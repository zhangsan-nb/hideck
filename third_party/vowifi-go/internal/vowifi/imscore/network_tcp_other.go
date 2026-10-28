//go:build !linux

package imscore

import "net"

func listenSystemIMSTCP(addr *net.TCPAddr, _ bool) (net.Listener, error) {
	return net.ListenTCP("tcp", addr)
}
