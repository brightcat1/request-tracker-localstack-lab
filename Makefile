.PHONY: infra-apply infra-destroy run-backend

infra-apply:
	cd infra/envs/local && tofu init && tofu apply -auto-approve

infra-destroy:
	cd infra/envs/local && tofu destroy -auto-approve

run-backend:
	cd backend && APP_ENV=local go run .