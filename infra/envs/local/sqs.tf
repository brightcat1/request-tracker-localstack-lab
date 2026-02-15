resource "aws_sqs_queue" "request_events" {
  name = "request-events"
}

output "request_events_queue_url" {
  value = aws_sqs_queue.request_events.url
}