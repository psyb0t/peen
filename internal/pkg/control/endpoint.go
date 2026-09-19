package control

import "net"

// loopbackHost dials a listener configured without an explicit host, such as
// ":8080", which is not a dialable address on its own.
const loopbackHost = "127.0.0.1"

// DialableAddress turns a configured listen address into one a client can
// connect to. A listener bound to every interface is written without a host,
// and that form cannot be dialed as written.
func DialableAddress(listenAddress string) string {
	host, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return listenAddress
	}

	if host == "" {
		return net.JoinHostPort(loopbackHost, port)
	}

	return listenAddress
}
