/*
Copyright © 2022 Netmaker Team <info@netmaker.io>
*/
package main

import (
	"log"
	"strings"

	"github.com/blang/semver"
	"github.com/gravitl/netclient/cmd"
	"github.com/gravitl/netclient/config"
	"github.com/gravitl/netclient/daemon"
	"github.com/rhysd/go-github-selfupdate/selfupdate"
)

var version = "dev"

func autoUpdate() {
	semVer := strings.Replace(version, "v", "", -1)
	v := semver.MustParse(semVer)
	latest, err := selfupdate.UpdateSelf(v, "gravitl/netmaker")
	if err != nil {
		log.Println("Binary update failed:", err)
		return
	}
	if !latest.Version.Equals(v) {
		log.Println("Successfully updated to version", latest.Version)
		log.Println("Release notes:\n", latest.ReleaseNotes)
		// reboot daemon
		if err := daemon.Stop(); err != nil {
			log.Println("Error encountered while stopping daemon:", err)
			return
		}
		if err := daemon.InstallDaemon(); err != nil {
			log.Println("Error encountered while installing daemon:", err)
			return
		}
		if err := daemon.Start(); err != nil {
			log.Println("Error encountered while starting daemon:", err)
			return
		}
	}
}

func main() {
	config.SetVersion(version)
	if version != "dev" {
		autoUpdate()
	}
	cmd.Execute()
}
