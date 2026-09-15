package main

import (
	"github.com/JosephWest2/cloud_dev/internal/cleanuplambda"
	"github.com/JosephWest2/cloud_dev/internal/expiry"
	"github.com/aws/aws-lambda-go/lambda"
	"os"
)

func main() {
	lambda.Start((cleanuplambda.Handler{Getenv: os.Getenv, Factory: cleanuplambda.AWSFactory, Clock: expiry.SystemClock{}}).Handle)
}
