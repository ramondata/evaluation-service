package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Evento que será enviado para a fila
type EvaluationEvent struct {
	UserID    string    `json:"user_id"`
	FlagName  string    `json:"flag_name"`
	Result    bool      `json:"result"`
	Timestamp time.Time `json:"timestamp"`
}

// sendEvaluationEvent envia um evento para a fila SQS
func (a *App) sendEvaluationEvent(ctx context.Context, userID, flagName string, result bool) {
	// Se a URL da fila não foi configurada, apenas loga localmente e sai.
	if a.SqsSvc == nil || a.SqsQueueURL == "" {
		log.Printf(
			"[SQS_DISABLED] Evento: User '%s', Flag '%s', Result '%t'",
			userID,
			flagName,
			result,
		)
		return
	}

	event := EvaluationEvent{
		UserID:    userID,
		FlagName:  flagName,
		Result:    result,
		Timestamp: time.Now().UTC(),
	}

	body, err := json.Marshal(event)
	if err != nil {
		log.Printf("Erro ao serializar evento SQS: %v", err)
		return
	}

	ctx, span := otel.Tracer("togglemaster").Start(ctx, "feature-evaluation-events publish", trace.WithSpanKind(trace.SpanKindProducer), trace.WithAttributes(attribute.String("messaging.system", "aws_sqs"), attribute.String("messaging.destination.name", "feature-evaluation-events")))
	defer span.End()
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	attrs := map[string]types.MessageAttributeValue{}
	for key, value := range carrier {
		attrs[key] = types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(value)}
	}
	// Envia a mensagem
	_, err = a.SqsSvc.SendMessage(
		ctx,
		&sqs.SendMessageInput{
			MessageBody:       aws.String(string(body)),
			MessageAttributes: attrs,
			QueueUrl:          aws.String(a.SqsQueueURL),
		},
	)

	if err != nil {
		log.Printf("Erro ao enviar mensagem para SQS: %v", err)
		return
	}

	log.Printf(
		"Evento de avaliação enviado para SQS (Flag: %s)",
		flagName,
	)
}
