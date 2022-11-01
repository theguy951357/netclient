package functions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/gravitl/netclient/config"
	"github.com/gravitl/netclient/local"
	"github.com/gravitl/netclient/ncutils"
	"github.com/gravitl/netclient/wireguard"
	"github.com/gravitl/netmaker/logger"
	"github.com/gravitl/netmaker/mq"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const lastNodeUpdate = "lnu"
const lastPeerUpdate = "lpu"

var messageCache = new(sync.Map)
var ServerSet map[string]mqtt.Client
var mqclient mqtt.Client

type cachedMessage struct {
	Message  string
	LastSeen time.Time
}

func Daemon() {
	logger.Log(0, "netclient daemon started -- version:", ncutils.Version)
	ServerSet = make(map[string]mqtt.Client)
	if err := ncutils.SavePID(); err != nil {
		logger.FatalLog("unable to save PID on daemon startup")
	}
	if err := local.SetIPForwarding(); err != nil {
		logger.Log(0, "unable to set IPForwarding", err.Error())
	}
	wg := sync.WaitGroup{}
	quit := make(chan os.Signal, 1)
	reset := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, os.Interrupt)
	signal.Notify(reset, syscall.SIGHUP)
	cancel := startGoRoutines(&wg)
	for {
		select {
		case <-quit:
			cancel()
			logger.Log(0, "shutting down netclient daemon")
			wg.Wait()
			if mqclient != nil {
				mqclient.Disconnect(250)
			}
			logger.Log(0, "shutdown complete")
			return
		case <-reset:
			logger.Log(0, "received reset")
			cancel()
			wg.Wait()
			if mqclient != nil {
				mqclient.Disconnect(250)
			}
			logger.Log(0, "restarting daemon")
			cancel = startGoRoutines(&wg)
		}
	}
}

func startGoRoutines(wg *sync.WaitGroup) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	//serverSet := make(map[string]bool)
	for _, node := range config.Nodes {
		if node.Connected {
			wireguard.ApplyConf(&node, config.GetNetclientInterfacePath()+node.Network+".conf")
		}
	}
	for _, server := range config.Servers {
		logger.Log(1, "started daemon for server ", server.Name)
		local.SetNetmakerDomainRoute(server.API)
		wg.Add(1)
		go messageQueue(ctx, wg, &server)
	}
	wg.Add(1)
	go Checkin(ctx, wg)
	return cancel
}

// sets up Message Queue and subsribes/publishes updates to/from server
// the client should subscribe to ALL nodes that exist on server locally
func messageQueue(ctx context.Context, wg *sync.WaitGroup, server *config.Server) {
	defer wg.Done()
	logger.Log(0, "netclient message queue started for server:", server.Name)
	err := setupMQTT(server)
	if err != nil {
		logger.Log(0, "unable to connect to broker", server.Broker, err.Error())
		return
	}
	//defer mqclient.Disconnect(250)
	<-ctx.Done()
	logger.Log(0, "shutting down message queue for server", server.Name)
}

// setupMQTT creates a connection to broker
// this function is used to create a connection to publish to the broker
func setupMQTT(server *config.Server) error {
	name, _ := os.Hostname()
	opts := mqtt.NewClientOptions()
	broker := server.Broker
	port := server.MQPort
	opts.AddBroker(fmt.Sprintf("mqtts://%s:%s", broker, port))
	opts.SetUsername(name)
	opts.SetPassword(server.Password)
	opts.SetClientID(ncutils.MakeRandomString(23))
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(time.Second << 2)
	opts.SetKeepAlive(time.Minute >> 1)
	opts.SetWriteTimeout(time.Minute)
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		for _, node := range config.Nodes {
			setSubscriptions(client, &node)
		}
	})
	opts.SetOrderMatters(true)
	opts.SetResumeSubs(true)
	opts.SetConnectionLostHandler(func(c mqtt.Client, e error) {
		logger.Log(0, "detected broker connection lost for", server.Broker)
	})
	mqclient = mqtt.NewClient(opts)
	ServerSet[server.Broker] = mqclient
	var connecterr error
	for count := 0; count < 3; count++ {
		connecterr = nil
		if token := mqclient.Connect(); !token.WaitTimeout(30*time.Second) || token.Error() != nil {
			logger.Log(0, "unable to connect to broker, retrying ...")
			if token.Error() == nil {
				connecterr = errors.New("connect timeout")
			} else {
				connecterr = token.Error()
			}
			if err := checkBroker(server.Broker, server.MQPort); err != nil {
				logger.Log(0, "could not connect to broker", server.Broker, err.Error())
			}
		}
	}
	if connecterr != nil {
		logger.Log(0, "failed to establish connection to broker: ", connecterr.Error())
		return connecterr
	}

	return nil
}

// sets MQ client subscriptions for a specific node config
// should be called for each node belonging to a given server
func setSubscriptions(client mqtt.Client, node *config.Node) {
	if token := client.Subscribe(fmt.Sprintf("update/%s/%s", node.Network, node.ID), 0, mqtt.MessageHandler(NodeUpdate)); token.WaitTimeout(mq.MQ_TIMEOUT*time.Second) && token.Error() != nil {
		if token.Error() == nil {
			logger.Log(0, "network:", node.Network, "connection timeout")
		} else {
			logger.Log(0, "network:", node.Network, token.Error().Error())
		}
		return
	}
	logger.Log(3, fmt.Sprintf("subscribed to node updates for node %s update/%s/%s", node.Name, node.Network, node.ID))
	if token := client.Subscribe(fmt.Sprintf("peers/%s/%s", node.Network, node.ID), 0, mqtt.MessageHandler(UpdatePeers)); token.Wait() && token.Error() != nil {
		logger.Log(0, "network", node.Network, token.Error().Error())
		return
	}
	logger.Log(3, fmt.Sprintf("subscribed to peer updates for node %s peers/%s/%s", node.Name, node.Network, node.ID))
}

// should only ever use node client configs
func decryptMsg(node *config.Node, msg []byte) ([]byte, error) {
	if len(msg) <= 24 { // make sure message is of appropriate length
		return nil, fmt.Errorf("recieved invalid message from broker %v", msg)
	}

	// setup the keys
	diskKey := node.TrafficPrivateKey

	serverPubKey, err := ncutils.ConvertBytesToKey(node.TrafficKeys.Server)
	if err != nil {
		return nil, err
	}
	return DeChunk(msg, serverPubKey, diskKey)
}

func read(network, which string) string {
	val, isok := messageCache.Load(fmt.Sprintf("%s%s", network, which))
	if isok {
		var readMessage = val.(cachedMessage) // fetch current cached message
		if readMessage.LastSeen.IsZero() {
			return ""
		}
		if time.Now().After(readMessage.LastSeen.Add(time.Hour * 24)) { // check if message has been there over a minute
			messageCache.Delete(fmt.Sprintf("%s%s", network, which)) // remove old message if expired
			return ""
		}
		return readMessage.Message // return current message if not expired
	}
	return ""
}

func insert(network, which, cache string) {
	var newMessage = cachedMessage{
		Message:  cache,
		LastSeen: time.Now(),
	}
	messageCache.Store(fmt.Sprintf("%s%s", network, which), newMessage)
}

// on a delete usually, pass in the nodecfg to unsubscribe client broker communications
// for the node in nodeCfg
func unsubscribeNode(client mqtt.Client, node *config.Node) {
	client.Unsubscribe(fmt.Sprintf("update/%s/%s", node.Network, node.ID))
	var ok = true
	if token := client.Unsubscribe(fmt.Sprintf("update/%s/%s", node.Network, node.ID)); token.WaitTimeout(mq.MQ_TIMEOUT*time.Second) && token.Error() != nil {
		if token.Error() == nil {
			logger.Log(1, "network:", node.Network, "unable to unsubscribe from updates for node ", node.Name, "\n", "connection timeout")
		} else {
			logger.Log(1, "network:", node.Network, "unable to unsubscribe from updates for node ", node.Name, "\n", token.Error().Error())
		}
		ok = false
	}
	if token := client.Unsubscribe(fmt.Sprintf("peers/%s/%s", node.Network, node.ID)); token.WaitTimeout(mq.MQ_TIMEOUT*time.Second) && token.Error() != nil {
		if token.Error() == nil {
			logger.Log(1, "network:", node.Network, "unable to unsubscribe from peer updates for node", node.Name, "\n", "connection timeout")
		} else {
			logger.Log(1, "network:", node.Network, "unable to unsubscribe from peer updates for node", node.Name, "\n", token.Error().Error())
		}
		ok = false
	}
	if ok {
		logger.Log(1, "network:", node.Network, "successfully unsubscribed node ", node.ID, " : ", node.Name)
	}
}

// UpdateKeys -- updates private key and returns new publickey
func UpdateKeys(node *config.Node, client mqtt.Client) error {
	var err error
	logger.Log(0, "interface:", node.Interface, "received message to update wireguard keys for network ", node.Network)
	node.PrivateKey, err = wgtypes.GeneratePrivateKey()
	if err != nil {
		logger.Log(0, "network:", node.Network, "error generating privatekey ", err.Error())
		return err
	}
	file := config.GetNetclientInterfacePath() + node.Interface + ".conf"
	if err := wireguard.UpdatePrivateKey(file, node.PrivateKey.String()); err != nil {
		logger.Log(0, "network:", node.Network, "error updating wireguard key ", err.Error())
		return err
	}
	node.PublicKey = node.PrivateKey.PublicKey()
	config.Nodes[node.Network] = *node
	if err := config.WriteNodeConfig(); err != nil {
		logger.Log(0, "error saving node", err.Error())
	}
	PublishNodeUpdate(node)
	return nil
}