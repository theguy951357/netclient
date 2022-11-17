//go:build linux || darwin || freebsd
// +build linux darwin freebsd

package wireguard

import (
	"net"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/ipc"
	"golang.zx2c4.com/wireguard/tun"
)

// == private ==

func (nc *NCIface) createUserSpaceWG() error {

	tunIface, err := tun.CreateTUN(nc.Settings.Interface, nc.Settings.MTU)
	if err != nil {
		return err
	}

	nc.Iface = tunIface

	tunDevice := device.NewDevice(tunIface, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, "[netclient] "))
	err = tunDevice.Up()
	if err != nil {
		return err
	}
	uapi, err := getUAPIByInterface(nc.Settings.Interface)
	if err != nil {
		return err
	}

	go func() {
		for {
			uapiConn, uapiErr := uapi.Accept()
			if uapiErr != nil {
				continue
			}
			go tunDevice.IpcHandle(uapiConn)
		}
	}()

	err = nc.ApplyAddrs()
	if err != nil {
		return err
	}
	return nil
}

func getUAPIByInterface(iface string) (net.Listener, error) {
	tunSock, err := ipc.UAPIOpen(iface)
	if err != nil {
		return nil, err
	}
	return ipc.UAPIListen(iface, tunSock)
}