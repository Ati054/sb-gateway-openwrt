package dnsproxy

import "net"

func dnsListenConfig() net.ListenConfig {
	var config net.ListenConfig
	// Independent of GODEBUG=multipathtcp and the Go runtime default.
	config.SetMultipathTCP(false)
	return config
}
