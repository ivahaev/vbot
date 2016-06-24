package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"sync"

	log "github.com/Sirupsen/logrus"
	"github.com/boltdb/bolt"
	"github.com/bot-api/telegram"
	"github.com/bot-api/telegram/telebot"
	"golang.org/x/net/context"
)

var (
	token                      string
	debug                      bool
	api                        *telegram.API
	bot                        *telebot.Bot
	db                         *bolt.DB
	subscribersBucket          = []byte("subscribers")
	subscribers                = map[int64]struct{}{}
	locker                     sync.RWMutex
	httpPort                   = "9090"
	requestMethodsRestrictions = []byte("Only GET and POST request is allowed")
)

func main() {
	if token = os.Getenv("VBOT_TOKEN"); token == "" {
		panic("Not token provided")
	}
	if os.Getenv("VBOT_DEBUG") != "" {
		debug = true
	}

	prepareDb()
	go startHTTPServer()

	api = telegram.New(token)
	bot = telebot.NewWithAPI(api)
	api.Debug(debug)

	bot.Use(telebot.Commands(map[string]telebot.Commander{
		"start": telebot.CommandFunc(
			func(ctx context.Context, arg string) error {
				api := telebot.GetAPI(ctx)
				update := telebot.GetUpdate(ctx)
				userID := update.Chat().ID
				locker.RLock()
				_, ok := subscribers[userID]
				locker.RUnlock()
				if ok {
					_, err := api.SendMessage(ctx,
						telegram.NewMessagef(userID, "Already subscribed"))
					return err
				}
				err := db.Update(func(tx *bolt.Tx) error {
					b, err := tx.CreateBucketIfNotExists(subscribersBucket)
					if err != nil {
						return err
					}
					encoded, err := json.Marshal(userID)
					if err != nil {
						return err
					}
					return b.Put(encoded, []byte(`true`))
				})
				if err != nil {
					_, err = api.SendMessage(ctx,
						telegram.NewMessagef(userID, "Can't subscibe you %v", err))
					return err
				}

				locker.Lock()
				subscribers[userID] = struct{}{}
				locker.Unlock()
				log.Debug(userID, arg)
				_, err = api.SendMessage(ctx,
					telegram.NewMessagef(userID,
						"You are subscribed to notifications",
					))
				return err
			}),
	}))

	netCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := bot.Serve(netCtx)
	if err != nil {
		panic(err)
	}
}

func prepareDb() {
	var err error
	db, err = bolt.Open("vbot.db", 0600, nil)
	if err != nil {
		panic(err)
	}
	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(subscribersBucket)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, _ []byte) error {
			var s int64
			err := json.Unmarshal(k, &s)
			if err != nil {
				return err
			}
			subscribers[s] = struct{}{}
			return nil
		})
	})
}

func broadcastMessage(msg string) {
	netCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for u := range subscribers {
		api.SendMessage(netCtx, telegram.NewMessagef(u, msg))
	}
}

func startHTTPServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", httpHandler)

	log.Infof("Start listening http requests at port %s", httpPort)
	err := http.ListenAndServe(":"+httpPort, mux)
	if err != nil {
		panic(err)
	}
}

func httpHandler(rw http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodPost:
		postHandler(rw, request)
	default:
		rw.WriteHeader(http.StatusBadRequest)
		rw.Write(requestMethodsRestrictions)
	}
}

func postHandler(rw http.ResponseWriter, request *http.Request) {
	buf := bytes.NewBuffer(nil)
	n, err := buf.ReadFrom(request.Body)
	if err != nil {
		rw.WriteHeader(http.StatusInternalServerError)
		rw.Write([]byte(err.Error()))
		return
	}
	if n == 0 {
		rw.WriteHeader(http.StatusBadRequest)
		rw.Write([]byte("empty request"))
		return
	}
	broadcastMessage(string(buf.Bytes()))
}
