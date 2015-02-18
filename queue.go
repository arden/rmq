package queue

import (
	"fmt"
	"log"
	"strings"

	redis "github.com/adjust/redis-latest-head" // TODO: update
	"github.com/adjust/uniuri"
)

const (
	connectionsKey                   = "rmq::connections"                                         // Set of connection names
	connectionHeartbeatTemplate      = "rmq::connection::{connection}::heartbeat"                 // expires after {connection} died
	connectionQueuesTemplate         = "rmq::connection::{connection}::queues"                    // Set of queues consumers of {connection} are consuming
	connectionQueueConsumersTemplate = "rmq::connection::{connection}::queue::{queue}::consumers" // Set of all consumers from {connection} consuming from {queue}
	connectionQueueUnackedTemplate   = "rmq::connection::{connection}::queue::{queue}::unacked"   // List of deliveries consumers of {connection} are currently consuming

	queuesKey          = "rmq::queues"                // Set of all open queues
	queueReadyTemplate = "rmq::queue::{queue}::ready" // List of deliveries in that {queue} (right is first and oldest, left is last and youngest)

	phConnection = "{connection}" // connection name
	phQueue      = "{queue}"      // queue name
	phConsumer   = "{consumer}"   // consumer name (consisting of tag and token)
)

type Queue struct {
	name           string
	connectionName string
	queuesKey      string // key to list of queues consumed by this connection
	consumersKey   string // key to set of consumers using this connection
	readyKey       string // key to list of ready deliveries
	unackedKey     string // key to list of currently consuming deliveries
	redisClient    *redis.Client
	deliveryChan   chan Delivery // nil for publish channels, not nil for consuming channels
}

func newQueue(name, connectionName, queuesKey string, redisClient *redis.Client) *Queue {
	unackedKey := strings.Replace(connectionQueueUnackedTemplate, phConnection, connectionName, 1)
	unackedKey = strings.Replace(unackedKey, phQueue, name, 1)

	consumersKey := strings.Replace(connectionQueueConsumersTemplate, phConnection, connectionName, 1)
	consumersKey = strings.Replace(consumersKey, phQueue, name, 1)

	readyKey := strings.Replace(queueReadyTemplate, phQueue, name, 1)

	queue := &Queue{
		name:           name,
		connectionName: connectionName,
		queuesKey:      queuesKey,
		consumersKey:   consumersKey,
		readyKey:       readyKey,
		unackedKey:     unackedKey,
		redisClient:    redisClient,
	}
	return queue
}

func (queue *Queue) String() string {
	return fmt.Sprintf("[%s conn:%s]", queue.name, queue.connectionName)
}

func (queue *Queue) Publish(payload string) error {
	return queue.redisClient.LPush(queue.readyKey, payload).Err()
}

func (queue *Queue) Purge() bool {
	result := queue.redisClient.Del(queue.readyKey)
	if result.Err() != nil {
		return false
	}
	return result.Val() > 0
}

func (queue *Queue) ReadyCount() int {
	result := queue.redisClient.LLen(queue.readyKey)
	if result.Err() != nil {
		log.Printf("queue failed to get ready count %s %s", queue, result.Err())
		return 0
	}
	return int(result.Val())
}

func (queue *Queue) UnackedCount() int {
	result := queue.redisClient.LLen(queue.unackedKey)
	if result.Err() != nil {
		log.Printf("queue failed to get unacked count %s %s", queue, result.Err())
		return 0
	}
	return int(result.Val())
}

// ReturnUnackedDeliveries moves all unacked deliveries back to the ready queue and deletes the unacked key afterwards
func (queue *Queue) ReturnUnackedDeliveries() (returned int, err error) {
	result := queue.redisClient.LLen(queue.unackedKey)
	if result.Err() != nil {
		return 0, fmt.Errorf("queue failed to get unacked count before returning %s %s", queue, result.Err())
	}

	unackedCount := int(result.Val())
	for i := 0; i < unackedCount; i++ {
		result := queue.redisClient.BRPopLPush(queue.unackedKey, queue.readyKey, 0)
		if result.Err() != nil {
			return 0, fmt.Errorf("queue failed to return unacked package %s", result.Err())
		}
	}

	result = queue.redisClient.LLen(queue.unackedKey)
	if result.Err() != nil {
		return 0, fmt.Errorf("queue failed to get unacked count after returning %s %s", queue, result.Err())
	}

	if result.Val() != 0 {
		return 0, fmt.Errorf("queue found new unacked packages after returning %s %d", queue, result.Val())
	}

	return unackedCount, nil
}

// CloseInConnection closes the queue in the associated connection by removing all related keys
func (queue *Queue) CloseInConnection() error {
	return queue.redisClient.Del(queue.unackedKey).Err()
}

func (queue *Queue) Clear() int {
	result := queue.redisClient.Del(queue.readyKey)
	if result.Err() != nil {
		log.Printf("queue failed to clear %s %s", queue, result.Err())
		return 0
	}
	return int(result.Val())
}

// PrepareConsumption starts consuming into a channel of size bufferSize
// must be called before consumers can be added!
func (queue *Queue) PrepareConsumption(bufferSize int) bool {
	if queue.deliveryChan != nil {
		return false
	}

	// add queue to list of queues consumed on this connection
	result := queue.redisClient.SAdd(queue.queuesKey, queue.name)
	if result.Err() != nil {
		log.Printf("queue failed to add itself %s %s", queue.name, result.Err())
		return false
	}

	queue.deliveryChan = make(chan Delivery, bufferSize)
	queue.redisClient.LPush(queue.queuesKey, queue.name)
	log.Printf("queue started consuming %s", queue)
	go queue.consume()
	return true
}

// AddConsumer adds a consumer to the queue and returns its internal name
// panics if PrepareConsumption wasn't called before!
func (queue *Queue) AddConsumer(tag string, consumer Consumer) string {
	if queue.deliveryChan == nil {
		log.Panicf("queue failed to add consumer, call PrepareConsumption first! %s", queue)
	}

	name := fmt.Sprintf("%s-%s", tag, uniuri.NewLen(6))

	// add consumer to list of consumers of this queue
	result := queue.redisClient.SAdd(queue.consumersKey, name)
	if result.Err() != nil {
		log.Printf("queue failed to add consumer %s %s", name, result.Err())
		return ""
	}

	go queue.addConsumer(consumer)
	log.Printf("queue added consumer %s %s %s", queue, name, queue.consumersKey)
	return name
}

func (queue *Queue) GetConsumers() []string {
	result := queue.redisClient.SMembers(queue.consumersKey)
	if result.Err() != nil {
		log.Printf("queue failed to get consumers %s", result.Err())
		return []string{}
	}
	return result.Val()
}

func (queue *Queue) RemoveConsumer(name string) bool {
	result := queue.redisClient.SRem(queue.consumersKey, name)
	if result.Err() != nil {
		log.Printf("queue failed to remove consumer %s %s %s", queue, name, result.Err())
		return false
	}
	return result.Val() > 0
}

func (queue *Queue) RemoveAllConsumers() int {
	result := queue.redisClient.Del(queue.consumersKey)
	if result.Err() != nil {
		log.Printf("queue failed to remove all consumers %s %s", queue, result.Err())
		return 0
	}
	return int(result.Val())
}

func (queue *Queue) consume() {
	for {
		result := queue.redisClient.BRPopLPush(queue.readyKey, queue.unackedKey, 0)
		if result.Err() != nil {
			log.Printf("queue failed to consume %s %s", queue, result.Err())
			continue
		}
		queue.deliveryChan <- newDelivery(result.Val(), queue.unackedKey, queue.redisClient)
	}
}

func (queue *Queue) addConsumer(consumer Consumer) {
	for delivery := range queue.deliveryChan {
		consumer.Consume(delivery)
	}
}
