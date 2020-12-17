package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"net/url"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	pod "github.com/icap-adaptation-service/pkg"
	"github.com/streadway/amqp"
)

const (
	ok        = "ok"
	jsonerr   = "json_error"
	k8sclient = "k8s_client_error"
	k8sapi    = "k8s_api_error"
)

var (
	exchange   = "adaptation-exchange"
	routingKey = "adaptation-request"
	queueName  = "adaptation-request-queue"

	procTime = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "gw_adaptation_message_processing_time_millisecond",
			Help:    "Time taken to process queue message",
			Buckets: []float64{5, 10, 100, 250, 500, 1000},
		},
	)

	msgTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gw_adaptation_messages_consumed_total",
			Help: "Number of messages consumed from Rabbit",
		},
		[]string{"status"},
	)

	podNamespace                          = os.Getenv("POD_NAMESPACE")
	inputMount                            = os.Getenv("INPUT_MOUNT")
	outputMount                           = os.Getenv("OUTPUT_MOUNT")
	requestProcessingImage                = os.Getenv("REQUEST_PROCESSING_IMAGE")
	requestProcessingTimeout              = os.Getenv("REQUEST_PROCESSING_TIMEOUT")
	adaptationRequestQueueHostname        = os.Getenv("ADAPTATION_REQUEST_QUEUE_HOSTNAME")
	adaptationRequestQueuePort            = os.Getenv("ADAPTATION_REQUEST_QUEUE_PORT")
	archiveAdaptationRequestQueueHostname = os.Getenv("ARCHIVE_ADAPTATION_QUEUE_REQUEST_HOSTNAME")
	archiveAdaptationRequestQueuePort     = os.Getenv("ARCHIVE_ADAPTATION_REQUEST_QUEUE_PORT")
	transactionEventQueueHostname         = os.Getenv("TRANSACTION_EVENT_QUEUE_HOSTNAME")
	transactionEventQueuePort             = os.Getenv("TRANSACTION_EVENT_QUEUE_PORT")
	messagebrokeruser                     = os.Getenv("MESSAGE_BROKER_USER")
	messagebrokerpassword                 = os.Getenv("MESSAGE_BROKER_PASSWORD")
	cpuLimit                              = os.Getenv("CPU_LIMIT")
	cpuRequest                            = os.Getenv("CPU_REQUEST")
	memoryLimit                           = os.Getenv("MEMORY_LIMIT")
	memoryRequest                         = os.Getenv("MEMORY_REQUEST")
)

func main() {
	if podNamespace == "" || inputMount == "" || outputMount == "" {
		log.Fatalf("init failed: POD_NAMESPACE, INPUT_MOUNT or OUTPUT_MOUNT environment variables not set")
	}

	if adaptationRequestQueueHostname == "" || archiveAdaptationRequestQueueHostname == "" || transactionEventQueueHostname == "" {
		log.Fatalf("init failed: ADAPTATION_REQUEST_QUEUE_HOSTNAME, ARCHIVE_ADAPTATION_QUEUE_REQUEST_HOSTNAME or TRANSACTION_EVENT_QUEUE_HOSTNAME environment variables not set")
	}

	if adaptationRequestQueuePort == "" || archiveAdaptationRequestQueuePort == "" || transactionEventQueuePort == "" {
		log.Fatalf("init failed: ADAPTATION_REQUEST_QUEUE_PORT, ARCHIVE_ADAPTATION_REQUEST_QUEUE_PORT or TRANSACTION_EVENT_QUEUE_PORT environment variables not set")
	}

	if cpuLimit == "" || cpuRequest == "" || memoryLimit == "" || memoryRequest == "" {
		log.Fatalf("init failed: CPU_LIMIT, CPU_REQUEST, MEMORY_LIMIT or MEMORY_REQUEST environment variables not set")
	}

	if messagebrokeruser == "" {
		messagebrokeruser = "guest"
	}

	if messagebrokerpassword == "" {
		messagebrokerpassword = "guest"
	}

	amqpUrl := url.URL{
		Scheme: "amqp",
		User:   url.UserPassword(messagebrokeruser, messagebrokerpassword),
		Host:   fmt.Sprintf("%s:%s", adaptationRequestQueueHostname, adaptationRequestQueuePort),
		Path:   "/",
	}
	fmt.Println("Connecting to ", amqpUrl.Host)

	conn, err := amqp.Dial(amqpUrl.String())
	failOnError(err, fmt.Sprintf("Failed to connect to %s", amqpUrl.Host))
	defer conn.Close()

	ch, err := conn.Channel()
	failOnError(err, "Failed to open a channel")
	defer ch.Close()

	err = ch.ExchangeDeclare(exchange, "direct", true, false, false, false, nil)
	failOnError(err, "Failed to declare an exchange")

	q, err := ch.QueueDeclare(queueName, false, false, false, false, nil)
	failOnError(err, "Failed to declare a queue")

	err = ch.QueueBind(q.Name, routingKey, exchange, false, nil)
	failOnError(err, "Failed to bind queue")

	msgs, err := ch.Consume(q.Name, "", true, false, false, false, nil)
	failOnError(err, "Failed to register a consumer")

	forever := make(chan bool)

	go func() {
		for d := range msgs {
			requeue, err := processMessage(d)
			if err != nil {
				log.Printf("Failed to process message: %v", err)
				ch.Nack(d.DeliveryTag, false, requeue)
			}
		}
	}()

	log.Printf("[*] Waiting for messages. To exit press CTRL+C")
	<-forever
}

func failOnError(err error, msg string) {
	if err != nil {
		log.Fatalf("%s: %s", msg, err)
	}
}

func processMessage(d amqp.Delivery) (bool, error) {
	defer func(start time.Time) {
		procTime.Observe(float64(time.Since(start).Milliseconds()))
	}(time.Now())

	if d.Headers["file-id"] == nil ||
		d.Headers["source-file-location"] == nil ||
		d.Headers["rebuilt-file-location"] == nil {
		return false, fmt.Errorf("Headers value is nil")
	}

	fileID := d.Headers["file-id"].(string)
	input := d.Headers["source-file-location"].(string)
	output := d.Headers["rebuilt-file-location"].(string)
	generateReport := "false"

	if d.Headers["generate-report"] != nil {
		generateReport = d.Headers["generate-report"].(string)
	}

	log.Printf("Received a message for file: %s", fileID)

	podArgs := pod.PodArgs{
		PodNamespace:                          podNamespace,
		FileID:                                fileID,
		Input:                                 input,
		Output:                                output,
		GenerateReport:                        generateReport,
		InputMount:                            inputMount,
		OutputMount:                           outputMount,
		ReplyTo:                               d.ReplyTo,
		RequestProcessingImage:                requestProcessingImage,
		RequestProcessingTimeout:              requestProcessingTimeout,
		AdaptationRequestQueueHostname:        adaptationRequestQueueHostname,
		AdaptationRequestQueuePort:            adaptationRequestQueuePort,
		ArchiveAdaptationRequestQueueHostname: archiveAdaptationRequestQueueHostname,
		ArchiveAdaptationRequestQueuePort:     archiveAdaptationRequestQueuePort,
		TransactionEventQueueHostname:         transactionEventQueueHostname,
		TransactionEventQueuePort:             transactionEventQueuePort,
		MessageBrokerUser:                     messagebrokeruser,
		MessageBrokerPassword:                 messagebrokerpassword,
		CPULimit:                              cpuLimit,
		CPURequest:                            cpuRequest,
		MemoryLimit:                           memoryLimit,
		MemoryRequest:                         memoryRequest,
	}

	err := podArgs.GetClient()
	if err != nil {
		msgTotal.WithLabelValues(k8sclient).Inc()
		return true, fmt.Errorf("Failed to get client for cluster: %v", err)
	}

	err = podArgs.CreatePod()
	if err != nil {
		msgTotal.WithLabelValues(k8sapi).Inc()
		return true, fmt.Errorf("Failed to create pod: %v", err)
	}

	msgTotal.WithLabelValues(ok).Inc()
	return false, nil
}
