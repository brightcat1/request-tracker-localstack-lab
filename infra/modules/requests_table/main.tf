resource "aws_dynamodb_table" "requests" {
  name         = "Requests"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "PK"

  attribute {
    name = "PK"
    type = "S"
  }
}
