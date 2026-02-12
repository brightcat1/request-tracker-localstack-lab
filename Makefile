.PHONY: infra-init infra-apply infra-destroy run-backend run-worker

TFDIR := infra/envs/local
APP_ENV := local

infra-init:
	tofu -chdir=$(TFDIR) init

infra-apply: infra-init
	tofu -chdir=$(TFDIR) fmt -recursive
	tofu -chdir=$(TFDIR) validate
	tofu -chdir=$(TFDIR) apply -auto-approve

infra-destroy: infra-init
	tofu -chdir=$(TFDIR) destroy -auto-approve

run-backend:
	cd backend && APP_ENV=$(APP_ENV) go run .

run-worker:
	cd backend && APP_ENV=$(APP_ENV) go run ./cmd/worker