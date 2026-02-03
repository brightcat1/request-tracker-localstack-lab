provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true

  endpoints {
    dynamodb = var.localstack_endpoint
    sts      = var.localstack_endpoint
  }
}

module "requests_table" {
  source = "../../modules/requests_table"
}

output "requests_table_name" {
  value = module.requests_table.table_name
}
