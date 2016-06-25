package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/boltdb/bolt"
	"github.com/bot-api/telegram"
	"github.com/bot-api/telegram/telebot"
	"golang.org/x/net/context"
)

var (
	token                      string
	debug                      bool
	apiKey                     string
	api                        *telegram.API
	bot                        *telebot.Bot
	db                         *bolt.DB
	subscribersBucket          = []byte("subscribers")
	subscribers                = map[int64]struct{}{}
	locker                     sync.RWMutex
	httpPort                   = "9090"
	requestMethodsRestrictions = []byte("Only POST request is allowed")
)

func main() {
	if token = os.Getenv("VBOT_TOKEN"); token == "" {
		panic("Not token provided")
	}
	if apiKey = os.Getenv("VBOT_KEY"); apiKey == "" {
		panic("Not apiKey provided")
	}
	if os.Getenv("VBOT_DEBUG") != "" {
		debug = true
		log.SetLevel(log.DebugLevel)
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
				log.WithField("userId", userID).Debug("New user subscribed")
				_, err = api.SendMessage(ctx,
					telegram.NewMessagef(userID,
						"You are subscribed to notifications",
					))
				return err
			}),
	}))

	netCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for {
		err := bot.Serve(netCtx)
		if err != nil {
			log.WithError(err).Warn("No connection to telegram")
		}
		time.Sleep(time.Millisecond * 200)
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
	log.WithField("msg", msg).Debug("Message received")
	netCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log.Debugf("Will send message to %d receivers", len(subscribers))
	locker.RLock()
	defer locker.RUnlock()
	for u := range subscribers {
		_, err := api.SendMessage(netCtx, telegram.NewMessagef(u, msg))
		if err != nil {
			log.WithError(err).Error("Can't send message to %d", u)
		}
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
	key := request.Header.Get("Authorization")
	if key == "" || !strings.Contains(key, "Basic") {
		rw.WriteHeader(http.StatusForbidden)
		rw.Write(nil)
		return
	}
	key = strings.TrimSpace(strings.TrimLeft(key, "Basic"))
	if key != apiKey {
		rw.WriteHeader(http.StatusForbidden)
		rw.Write(nil)
		return
	}
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
