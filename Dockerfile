FROM golang:1.23-alpine

WORKDIR /app

COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY main.go .

RUN go build -o server main.go

EXPOSE 8080
CMD ["./server"]