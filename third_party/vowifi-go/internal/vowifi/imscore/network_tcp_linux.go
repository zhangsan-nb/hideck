package imscore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"

	"github.com/iniwex5/vowifi-go/internal/vowifi/logging"
	"golang.org/x/sys/unix"
)

func listenSystemIMSTCP(addr *net.TCPAddr, allowFallback bool) (net.Listener, error) {
	listener, err := net.ListenTCP("tcp", addr)
	if err != nil || !allowFallback {
		return listener, err
	}
	fallback, err := listenerNeedsMPTCPMSSFallback(listener)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("imscore: check MPTCP listener MSS: %w", err), listener.Close())
	}
	if !fallback {
		return listener, nil
	}
	if err := listener.Close(); err != nil {
		return nil, fmt.Errorf("imscore: close incompatible MPTCP listener: %w", err)
	}
	logging.WarnRate("ims-mptcp-mss", "IMS MPTCP listener cannot set TCP_MAXSEG; retrying with TCP",
		"err", syscall.EOPNOTSUPP, "mss", ipsec3gppTCPMSS)
	var config net.ListenConfig
	config.SetMultipathTCP(false)
	if addr == nil {
		addr = &net.TCPAddr{}
	}
	return config.Listen(context.Background(), "tcp", addr.String())
}

func listenerNeedsMPTCPMSSFallback(listener *net.TCPListener) (bool, error) {
	raw, err := listener.SyscallConn()
	if err != nil {
		return false, err
	}
	var fallback bool
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		protocol, err := syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_PROTOCOL)
		if err != nil {
			socketErr = err
			return
		}
		fallback, socketErr = mptcpMSSNeedsTCP(protocol, func() error {
			return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_MAXSEG, ipsec3gppTCPMSS)
		})
	}); err != nil {
		return false, err
	}
	return fallback, socketErr
}

// Probe the actual socket option, not the kernel version. Ordinary TCP and
// MPTCP implementations that support the required MSS keep their original protocol.
func mptcpMSSNeedsTCP(protocol int, setMSS func() error) (bool, error) {
	if protocol != unix.IPPROTO_MPTCP {
		return false, nil
	}
	err := setMSS()
	if errors.Is(err, syscall.EOPNOTSUPP) {
		return true, nil
	}
	return false, err
}
