package lnd

import (
	"context"
	"encoding/hex"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/routerrpc"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/flokiorg/lokihub/lspd/ports"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type LndHtlcInterceptor struct {
	interceptor   ports.InterceptHandler
	config        *config.NodeConfigRef
	client        *LndClient
	stopRequested bool
	initWg        sync.WaitGroup
	doneWg        sync.WaitGroup
	ctx           context.Context
	cancel        context.CancelFunc
}

func NewLndHtlcInterceptor(
	conf *config.NodeConfigRef,
	client *LndClient,
	interceptor ports.InterceptHandler,
) (*LndHtlcInterceptor, error) {
	i := &LndHtlcInterceptor{
		config:      conf,
		client:      client,
		interceptor: interceptor,
	}

	i.initWg.Add(1)

	return i, nil
}

func (i *LndHtlcInterceptor) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	i.ctx = ctx
	i.cancel = cancel
	i.stopRequested = false

	return i.intercept()
}

func (i *LndHtlcInterceptor) Stop() error {
	// Setting stopRequested to true will make the interceptor stop receiving.
	i.stopRequested = true

	// Wait until all already received htlcs are handled, responses sent back.
	i.doneWg.Wait()

	// Close the grpc connection.
	i.cancel()
	return nil
}

func (i *LndHtlcInterceptor) WaitStarted() {
	i.initWg.Wait()
}

func (i *LndHtlcInterceptor) intercept() error {
	inited := false
	defer func() {
		if !inited {
			i.initWg.Done()
		}
		log.Debug().Msg("LND: Interceptor stopping, waiting for in-progress tasks")
		i.doneWg.Wait()
	}()

	for {
		if i.ctx.Err() != nil {
			return i.ctx.Err()
		}

		log.Info().Msg("LND: Connecting HTLC interceptor")
		interceptorClient, err := i.client.routerClient.HtlcInterceptor(i.ctx)
		if err != nil {
			log.Error().Msgf("routerClient.HtlcInterceptor(): %v", err)
			<-time.After(time.Second)
			continue
		}

		for {
			if i.ctx.Err() != nil {
				return i.ctx.Err()
			}

			if !inited {
				inited = true
				i.initWg.Done()
			}

			// Stop receiving if stop if requested. The defer func on top of this
			// function will assure all htlcs that are currently being processed
			// will complete.
			if i.stopRequested {
				return nil
			}

			request, err := interceptorClient.Recv()
			if err != nil {
				// If it is  just the error result of the context cancellation
				// the we exit silently.
				status, ok := status.FromError(err)
				if ok && status.Code() == codes.Canceled {
					log.Debug().Msg("LND: Interceptor stream closed - context canceled")
					break
				}

				// Otherwise it an unexpected error, we fail the test.
				log.Error().Msgf("unexpected error in interceptor.Recv() %v", err)
				break
			}

			i.doneWg.Add(1)
			go func() {
				scid := lightning.ShortChannelID(request.OutgoingRequestedChanId)
				interceptResult := i.interceptor.Intercept(core.InterceptRequest{
					Identifier:          request.IncomingCircuitKey.String(),
					Scid:                scid,
					PaymentHash:         request.PaymentHash,
					IncomingAmountMloki: request.IncomingAmountMsat,
					OutgoingAmountMloki: request.OutgoingAmountMsat,
					IncomingExpiry:      request.IncomingExpiry,
					OutgoingExpiry:      request.OutgoingExpiry,
				})
				switch interceptResult.Action {
				case core.INTERCEPT_RESUME_WITH_ONION:
					phantomSCID := request.OutgoingRequestedChanId
					realSCID := uint64(interceptResult.Scid)
					if phantomSCID != 0 && phantomSCID != realSCID {
						if err := i.client.RegisterChannelAlias(i.ctx, realSCID, phantomSCID); err != nil {
							log.Warn().Err(err).
								Uint64("phantom_scid", phantomSCID).
								Uint64("real_scid", realSCID).
								Msg("Interceptor: failed to register phantom SCID alias — payment may fail")
						}
					}
					interceptorClient.Send(i.createOnionResponse(interceptResult, request))
				case core.INTERCEPT_FAIL_HTLC_WITH_CODE:
					log.Error().Str("payment_hash", hex.EncodeToString(request.PaymentHash)).Msgf("LND: Failing HTLC with code %x", interceptResult.FailureCode)
					interceptorClient.Send(&routerrpc.ForwardHtlcInterceptResponse{
						IncomingCircuitKey: request.IncomingCircuitKey,
						Action:             routerrpc.ResolveHoldForwardAction_FAIL,
						FailureCode:        i.mapFailureCode(interceptResult.FailureCode),
					})
				case core.INTERCEPT_RESUME:
					fallthrough
				default:
					interceptorClient.Send(&routerrpc.ForwardHtlcInterceptResponse{
						IncomingCircuitKey: request.IncomingCircuitKey,
						Action:             routerrpc.ResolveHoldForwardAction_RESUME,
					})
				}

				i.doneWg.Done()
			}()
		}

		<-time.After(time.Second)
	}
}

// mapFailureCode translates flspd's own InterceptFailureCode into the
// lnrpc.Failure_FailureCode flnd's ForwardHtlcInterceptResponse expects.
//
// flnd's htlcswitch (interceptable_switch.go's interceptedForward.FailWithCode)
// only actually implements FailWithCode for a handful of lnwire codes —
// CodeTemporaryChannelFailure, the three CodeInvalidOnion* variants, and
// CodeExpiryTooSoon — and returns ErrUnsupportedFailureCode for anything
// else. Sending an unsupported code doesn't just fail that one HTLC: it
// breaks the entire interceptor gRPC stream (observed as "unexpected error
// in interceptor.Recv()"), which reconnects and redelivers the same
// still-pending HTLC, hitting the same error again — an infinite loop that
// starves every other HTLC being intercepted at the time until the
// sender's own payment timeout gives up. So every InterceptFailureCode
// value maps to TEMPORARY_CHANNEL_FAILURE, the one code this interceptor
// path can actually deliver, regardless of which flspd-level rejection
// reason triggered it.
func (i *LndHtlcInterceptor) mapFailureCode(original core.InterceptFailureCode) lnrpc.Failure_FailureCode {
	if original != core.FAILURE_TEMPORARY_CHANNEL_FAILURE {
		log.Debug().Msgf("LND: failure code %v has no working flnd interceptor mapping, using temporary channel failure", original)
	}
	return lnrpc.Failure_TEMPORARY_CHANNEL_FAILURE
}

func (i *LndHtlcInterceptor) createOnionResponse(interceptResult core.InterceptResult, request *routerrpc.ForwardHtlcInterceptRequest) *routerrpc.ForwardHtlcInterceptResponse {
	return &routerrpc.ForwardHtlcInterceptResponse{
		IncomingCircuitKey: request.IncomingCircuitKey,
		Action:             routerrpc.ResolveHoldForwardAction_RESUME_MODIFIED,
		OutAmountMsat:      interceptResult.AmountMloki,
	}
}
