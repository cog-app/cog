FROM golang:1.26-alpine AS build
RUN apk add --no-cache git gcc musl-dev
RUN git config --global --add safe.directory '*'

COPY . /code
WORKDIR /code
RUN CGO_ENABLED=1 go generate ./...
RUN CGO_ENABLED=1 go build ./cmd/cog

FROM alpine
RUN apk add --no-cache \
	libreoffice \
	tesseract-ocr \
	tesseract-ocr-data-eng \
	ffmpeg \
	ca-certificates
COPY --from=build /code/cog /usr/bin/cog
EXPOSE 8580
CMD [ "/usr/bin/cog", "--addr", ":8580", "--db", "/data/system.db", "--data-dir", "/data" ]
