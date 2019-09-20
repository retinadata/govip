#!/bin/bash
if [ $# -eq 0 ]; then
	echo Specify version
	exit 1
fi
go build -o govip -ldflags "-X main.Version=$1"
