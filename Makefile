.PHONY: help build run stop clean push pull test logs

# Variables
IMAGE_NAME = ghcr.io/r0ld3x/flashmeet
VERSION ?= latest
LOCAL_IMAGE = flashmeet:local

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build Docker image locally
	@echo "Building Docker image..."
	docker build -t $(LOCAL_IMAGE) .
	@echo "✅ Image built: $(LOCAL_IMAGE)"

build-prod: ## Build production image with optimizations
	@echo "Building production Docker image..."
	docker buildx build --platform linux/amd64,linux/arm64 -t $(IMAGE_NAME):$(VERSION) .
	@echo "✅ Image built: $(IMAGE_NAME):$(VERSION)"

run: ## Run with docker-compose
	@echo "Starting flashmeet with docker-compose..."
	docker-compose up -d
	@echo "✅ flashmeet is running at http://localhost:8080"

stop: ## Stop docker-compose services
	@echo "Stopping services..."
	docker-compose down
	@echo "✅ Services stopped"

clean: ## Stop and remove all containers, volumes, and images
	@echo "Cleaning up..."
	docker-compose down -v --rmi all
	@echo "✅ Cleanup complete"

logs: ## Show logs from docker-compose
	docker-compose logs -f

logs-app: ## Show logs from app container only
	docker-compose logs -f app

push: ## Push image to GHCR (requires authentication)
	@echo "Pushing to GHCR..."
	docker push $(IMAGE_NAME):$(VERSION)
	@echo "✅ Image pushed: $(IMAGE_NAME):$(VERSION)"

pull: ## Pull image from GHCR
	@echo "Pulling from GHCR..."
	docker pull $(IMAGE_NAME):$(VERSION)
	@echo "✅ Image pulled: $(IMAGE_NAME):$(VERSION)"

test: build ## Build and test the image locally
	@echo "Testing Docker image..."
	docker run --rm -p 8080:8080 -e REDIS_HOST=localhost $(LOCAL_IMAGE) &
	@sleep 2
	@curl -f http://localhost:8080 || (echo "❌ Health check failed" && exit 1)
	@echo "✅ Health check passed"

shell: ## Open shell in running app container
	docker-compose exec app sh

redis-cli: ## Open Redis CLI
	docker-compose exec redis redis-cli

ps: ## Show running containers
	docker-compose ps

restart: stop run ## Restart all services

rebuild: ## Rebuild and restart
	@echo "Rebuilding..."
	docker-compose up -d --build
	@echo "✅ Rebuild complete"

size: ## Show image size
	@echo "Image sizes:"
	@docker images | grep -E "flashmeet|IMAGE"

prune: ## Remove unused Docker resources
	@echo "Pruning Docker resources..."
	docker system prune -af --volumes
	@echo "✅ Prune complete"

