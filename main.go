// Copyright 2020 retinadata

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

//     http://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/j-keck/arping"
	"github.com/vishvananda/netlink"
	etcd "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
	"go.etcd.io/etcd/pkg/transport"
	"go.uber.org/zap"
)

var (
	Version     = "Not defined"
	version     = flag.Bool("version", false, "Print version and exit")
	prefix      = flag.String("name", "/govip/", "Position to synchronize multiple govips")
	member      = flag.String("member", "hostname", "Unique name for this govip")
	vip         = flag.String("vip", "192.168.0.254/32", "VIP to announce from the selected govip")
	vif         = flag.String("vif", "eth0", "Interface to announce the VIP from")
	etcdaddress = flag.String("etcd", "127.0.0.1:2379", "etcd address(es)")
	cafile      = flag.String("cacert", "", "etcd CA cert")
	certfile    = flag.String("cert", "", "etcd cert file")
	keyfile     = flag.String("key", "", "etcd key file")
)

// Global logger
var log *zap.Logger

func hasIP() (bool, *netlink.Addr, netlink.Link, error) {
	vaddr, err := netlink.ParseAddr(*vip)
	if err != nil {
		return false, nil, nil, err
	}
	vlink, err := netlink.LinkByName(*vif)
	if err != nil {
		return false, nil, nil, err
	}
	addrs, err := netlink.AddrList(vlink, netlink.FAMILY_ALL)
	if err != nil {
		return false, nil, nil, err
	}

	for _, addr := range addrs {
		if vaddr.Equal(addr) {
			return true, vaddr, vlink, nil
		}
	}
	return false, vaddr, vlink, nil
}

func releaseIP() error {
	log.Debug("Releasing IP address")
	set, vaddr, vlink, err := hasIP()
	if err != nil {
		return err
	}
	if !set {
		log.Debug("IP address not found")
		return nil
	}
	if err := netlink.AddrDel(vlink, vaddr); err != nil {
		return err
	}
	log.Info("IP address released")
	return nil
}

func ensureIP() (bool, error) {
	log.Debug("Ensuring IP address")
	set, vaddr, vlink, err := hasIP()
	if err != nil {
		return false, err
	}
	if set {
		log.Debug("IP address already set")
		return false, nil
	}
	if err := netlink.AddrAdd(vlink, vaddr); err != nil {
		return false, err
	}
	log.Info("IP address set, sending gratuitous ARPs")
	for range 5 {
		arping.GratuitousArpOverIfaceByName(vaddr.IP, *vif)
		time.Sleep(1 * time.Second)
	}

	return true, nil
}

func checkSuperuser() {
	// Check if running with superuser privileges
	if os.Geteuid() != 0 {
		fmt.Println("Error: This program must be run with superuser privileges (sudo)")
		os.Exit(1)
	}
}

func etcdClient() (*etcd.Client, error) {
	// Validate cert and key parameters - they must be provided together
	if (*certfile != "" && *keyfile == "") || (*certfile == "" && *keyfile != "") {
		return nil, errors.New("Both cert and key must be provided together")
	}

	// Create etcd client with or without TLS based on provided parameters
	var tlsConfig *tls.Config
	var err error
	if *certfile != "" || *cafile != "" {
		// Configure TLS
		tlsInfo := transport.TLSInfo{
			CertFile:      *certfile,
			KeyFile:       *keyfile,
			TrustedCAFile: *cafile,
		}
		tlsConfig, err = tlsInfo.ClientConfig()
		if err != nil {
			return nil, err
		}
	}

	return etcd.New(etcd.Config{
		Endpoints:            strings.Split(*etcdaddress, ","),
		DialTimeout:          5 * time.Second,
		DialKeepAliveTime:    5 * time.Second,
		DialKeepAliveTimeout: 5 * time.Second,
		TLS:                  tlsConfig,
	})
}

func main() {

	flag.Parse()
	if *version {
		fmt.Println(Version)
		return
	}

	checkSuperuser()

	var err error

	// Initialize zap logger
	config := zap.NewProductionConfig()
	log, err = config.Build()
	if err != nil {
		fmt.Printf("Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}

	log.Info("govip starting...")

	// To ensure clean state
	releaseIP()

	cli, err := etcdClient()

	if err != nil {
		log.Fatal("Failed to create etcd client", zap.Error(err))
	}
	defer cli.Close() // make sure to close the client

	stop := make(chan int)
	exit := make(chan int)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		returnCode := 0
		defer func() { exit <- returnCode }()
		log.Info("Getting etcd session...")
		s, err := concurrency.NewSession(cli, concurrency.WithContext(ctx))
		if err != nil {
			log.Error("Failed to get session", zap.Error(err))
			returnCode = 1
			return
		}
		closing := false
		defer func() {
			closing = true
			s.Close()
		}()

		// Un-block Campaign() if connection is lost
		go func() {
			<-s.Done()
			if !closing { // Unexpected close
				log.Error("Lease is invalid")
				cancel()
			}
		}()

		e := concurrency.NewElection(s, *prefix)

		for { // Keep doing this because IP can be removed manually
			log.Debug("Waiting to become the leader...")
			err := e.Campaign(ctx, *member)
			if err == context.Canceled {
				log.Debug("Context canceled while waiting for leader")
				return
			}
			if err != nil {
				log.Error("Campaign failed", zap.Error(err))
				returnCode = 1
				return
			}
			log.Debug("I am the leader")

			_, err = ensureIP()
			if err != nil {
				log.Error("Failed to ensure IP", zap.Error(err))
				returnCode = 1
				return
			}
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()

	// Exit on syscalls
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan,
		syscall.SIGINT,
		syscall.SIGTERM)
	go func() {
		s := <-signalChan
		log.Info("Stopping govip...", zap.String("signal", s.String()))
		close(stop)
	}()

	code := <-exit
	cli.Close()
	cancel()
	releaseIP()
	log.Sync()
	os.Exit(code)
}
