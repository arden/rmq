package main

import (
	"fmt"
	"github.com/adjust/rmq/v4"
	"github.com/go-redis/redis/v8"
	"log"
)

const (
	numDeliveries = 10
)

func main() {
	redisClient := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "123456", // no password set
		DB:       0,  // use default DB
	})
	connection, err := rmq.OpenConnectionWithRedisClient("producer", redisClient, nil)

	if err != nil {
		panic(err)
	}

	things, err := connection.OpenQueue("things")
	if err != nil {
		panic(err)
	}

	for i := 0; i < numDeliveries; i++ {
		delivery := fmt.Sprintf("delivery %d", i)
		if err := things.Publish(delivery); err != nil {
			log.Printf("failed to publish: %s", err)
		}
	}
}
