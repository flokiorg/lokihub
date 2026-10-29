# Cash: what a Hub and its relays can see

A Cash Hub serves bills over [NIP-CASH](https://github.com/ohstr/nips/blob/main/NIP-CASH.md)'s private
transport. That transport hides what a request is about and which bill it touches. It does not hide
everything, and this page lists what is left visible, so operators and holders can decide with the full
picture.

## One inbox per Hub

Every request to a Hub goes to the same inbox, the one its public kind-11190 announcement names. That is a
deliberate trade. A single, announced inbox is what lets a client start from nothing but a Hub connection
string. The cost is that a relay carrying the traffic sees the pair (client network address, this Hub). It
cannot tell which bill, which method or whose slice. It can tell that this address talks to this Hub.

A holder who needs that pair hidden should reach the relays over a network layer that hides their address,
such as Tor or a proxy. That reduces what a relay learns to "somebody is using this Hub".

The operator can rotate the inbox (`POST /api/private-transport/rotate-inbox`): the Hub moves to its next
inbox key and announces it. The old key keeps opening requests for `max_request_age` after the rotation, so
a request built before the new announcement still runs, and is then dropped. A circle wallet notes the inbox
key it was issued under, and the Hub keeps reading hidden members under it while that wallet lives.

## Consolidating tells the Hub the bills share an owner

`cash_consolidate` merges several slices into one new bill. To do that, the Hub must see one caller prove
ownership of every source. A consolidation is therefore a statement, to the Hub, that those bills had one
holder. A holder who does not want two bills linked should not consolidate them.

Lokihub keeps that link. When a bill is destroyed, its archive row records each slice's outcome, and for a
slice that moved into another bill (a split or a consolidation) it records which bill it went to. The
archive is kept indefinitely, because it is the operator's only record that the money passed through.
NIP-CASH asks a Hub not to keep the source-to-target link longer than its own audit policy needs, so an
operator with a shorter policy should prune those archive rows themselves.

## Destroyed bills and the retention window

A destroyed bill can still answer `cash_status` for a while, so a holder can tell "spent" from "never
existed". How long is the Cash Hub's retention setting. A Cash Hub created before that setting existed has a
retention of 0, so its destroyed bills answer nothing. Lokihub does not change that for you, because 0 is
also a valid deliberate choice. Set a retention in the hub's settings if you want those bills to answer.

## Related reading

- **[NIP-CASH §Privacy Considerations](https://github.com/ohstr/nips/blob/main/NIP-CASH.md)**: the
  protocol's own account of these trades.
- **[Services](lokihub-services.md)**: where the relays this traffic travels over are configured.
