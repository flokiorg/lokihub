package itest

import "net"

// getPort finds a free TCP port by briefly binding to port 0 and releasing
// it. Shared by FlspdProcess (gRPC/dashboard listen addresses) and
// PostgresContainer (host port mapping).
func getPort() (uint32, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return uint32(l.Addr().(*net.TCPAddr).Port), nil
}
