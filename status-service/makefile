build:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o chotu-status .

docker:
	docker build -t chotu-status .

run:
	docker run -d --name chotu-status -p 8081:8081 chotu-status

stop:
	docker stop chotu-status