build:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o chotu-status .

docker:
	docker build -t chotu-status .

run:
	docker run --rm -p 8081:8081 chotu-status