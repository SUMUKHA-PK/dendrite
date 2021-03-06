// Copyright 2020 The Matrix.org Foundation C.I.C.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// +build wasm

package main

import (
	"crypto/ed25519"
	"fmt"
	"net/http"

	"github.com/matrix-org/dendrite/appservice"
	"github.com/matrix-org/dendrite/clientapi"
	"github.com/matrix-org/dendrite/common"
	"github.com/matrix-org/dendrite/common/basecomponent"
	"github.com/matrix-org/dendrite/common/config"
	"github.com/matrix-org/dendrite/common/transactions"
	"github.com/matrix-org/dendrite/federationapi"
	"github.com/matrix-org/dendrite/federationsender"
	"github.com/matrix-org/dendrite/mediaapi"
	"github.com/matrix-org/dendrite/publicroomsapi"
	"github.com/matrix-org/dendrite/roomserver"
	"github.com/matrix-org/dendrite/syncapi"
	"github.com/matrix-org/dendrite/typingserver"
	"github.com/matrix-org/dendrite/typingserver/cache"
	"github.com/matrix-org/go-http-js-libp2p/go_http_js_libp2p"
	"github.com/matrix-org/gomatrixserverlib"

	"github.com/sirupsen/logrus"

	_ "github.com/matrix-org/go-sqlite3-js"
)

func init() {
	fmt.Println("dendrite.js starting...")
}

func generateKey() ed25519.PrivateKey {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		logrus.Fatalf("Failed to generate ed25519 key: %s", err)
	}
	return priv
}

func createFederationClient(cfg *config.Dendrite, node *go_http_js_libp2p.P2pLocalNode) *gomatrixserverlib.FederationClient {
	fmt.Println("Running in js-libp2p federation mode")
	fmt.Println("Warning: Federation with non-libp2p homeservers will not work in this mode yet!")
	tr := go_http_js_libp2p.NewP2pTransport(node)

	fed := gomatrixserverlib.NewFederationClient(
		cfg.Matrix.ServerName, cfg.Matrix.KeyID, cfg.Matrix.PrivateKey,
	)
	fed.Client = *gomatrixserverlib.NewClientWithTransport(tr)

	return fed
}

func createP2PNode(privKey ed25519.PrivateKey) (serverName string, node *go_http_js_libp2p.P2pLocalNode) {
	hosted := "/dns4/rendezvous.matrix.org/tcp/8443/wss/p2p-websocket-star/"
	node = go_http_js_libp2p.NewP2pLocalNode("org.matrix.p2p.experiment", privKey.Seed(), []string{hosted})
	serverName = node.Id
	fmt.Println("p2p assigned ServerName: ", serverName)
	return
}

func main() {
	cfg := &config.Dendrite{}
	cfg.SetDefaults()
	cfg.Kafka.UseNaffka = true
	cfg.Database.Account = "file:dendritejs_account.db"
	cfg.Database.AppService = "file:dendritejs_appservice.db"
	cfg.Database.Device = "file:dendritejs_device.db"
	cfg.Database.FederationSender = "file:dendritejs_fedsender.db"
	cfg.Database.MediaAPI = "file:dendritejs_mediaapi.db"
	cfg.Database.Naffka = "file:dendritejs_naffka.db"
	cfg.Database.PublicRoomsAPI = "file:dendritejs_publicrooms.db"
	cfg.Database.RoomServer = "file:dendritejs_roomserver.db"
	cfg.Database.ServerKey = "file:dendritejs_serverkey.db"
	cfg.Database.SyncAPI = "file:dendritejs_syncapi.db"
	cfg.Kafka.Topics.UserUpdates = "user_updates"
	cfg.Kafka.Topics.OutputTypingEvent = "output_typing_event"
	cfg.Kafka.Topics.OutputClientData = "output_client_data"
	cfg.Kafka.Topics.OutputRoomEvent = "output_room_event"
	cfg.Matrix.TrustedIDServers = []string{
		"matrix.org", "vector.im",
	}
	cfg.Matrix.KeyID = libp2pMatrixKeyID
	cfg.Matrix.PrivateKey = generateKey()

	serverName, node := createP2PNode(cfg.Matrix.PrivateKey)
	cfg.Matrix.ServerName = gomatrixserverlib.ServerName(serverName)

	if err := cfg.Derive(); err != nil {
		logrus.Fatalf("Failed to derive values from config: %s", err)
	}
	base := basecomponent.NewBaseDendrite(cfg, "Monolith")
	defer base.Close() // nolint: errcheck

	accountDB := base.CreateAccountsDB()
	deviceDB := base.CreateDeviceDB()
	keyDB := base.CreateKeyDB()
	federation := createFederationClient(cfg, node)
	keyRing := gomatrixserverlib.KeyRing{
		KeyFetchers: []gomatrixserverlib.KeyFetcher{
			&libp2pKeyFetcher{},
		},
		KeyDatabase: keyDB,
	}
	p2pPublicRoomProvider := NewLibP2PPublicRoomsProvider(node)

	alias, input, query := roomserver.SetupRoomServerComponent(base)
	typingInputAPI := typingserver.SetupTypingServerComponent(base, cache.NewTypingCache())
	asQuery := appservice.SetupAppServiceAPIComponent(
		base, accountDB, deviceDB, federation, alias, query, transactions.New(),
	)
	fedSenderAPI := federationsender.SetupFederationSenderComponent(base, federation, query)

	clientapi.SetupClientAPIComponent(
		base, deviceDB, accountDB,
		federation, &keyRing, alias, input, query,
		typingInputAPI, asQuery, transactions.New(), fedSenderAPI,
	)
	federationapi.SetupFederationAPIComponent(base, accountDB, deviceDB, federation, &keyRing, alias, input, query, asQuery, fedSenderAPI)
	mediaapi.SetupMediaAPIComponent(base, deviceDB)
	publicroomsapi.SetupPublicRoomsAPIComponent(base, deviceDB, query, federation, p2pPublicRoomProvider)
	syncapi.SetupSyncAPIComponent(base, deviceDB, accountDB, query, federation, cfg)

	httpHandler := common.WrapHandlerInCORS(base.APIMux)

	http.Handle("/", httpHandler)

	// Expose the matrix APIs via libp2p-js - for federation traffic
	if node != nil {
		go func() {
			logrus.Info("Listening on libp2p-js host ID ", node.Id)
			s := JSServer{
				Mux: http.DefaultServeMux,
			}
			s.ListenAndServe("p2p")
		}()
	}

	// Expose the matrix APIs via fetch - for local traffic
	go func() {
		logrus.Info("Listening for service-worker fetch traffic")
		s := JSServer{
			Mux: http.DefaultServeMux,
		}
		s.ListenAndServe("fetch")
	}()

	// We want to block forever to let the fetch and libp2p handler serve the APIs
	select {}
}
