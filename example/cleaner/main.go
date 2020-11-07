package main

import (
	"log"
	"time"

	"github.com/adjust/rmq/v4"
	"github.com/go-redis/redis/v8"

)

func main() {
	redisClient := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "123456", // no password set
		DB:       0,  // use default DB
	})
	connection, err := rmq.OpenConnectionWithRedisClient("cleaner", redisClient, nil)
	if err != nil {
		panic(err)
	}

	cleaner := rmq.NewCleaner(connection)

	for range time.Tick(time.Second) {
		returned, err := cleaner.Clean()
		if err != nil {
			log.Printf("failed to clean: %s", err)
			continue
		}
		log.Printf("cleaned %d", returned)
	}
}
