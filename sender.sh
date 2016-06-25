#!/bin/bash

curl -X POST http://localhost:9090/ -d $"`verlog`" --header "Authorization: Basic $VBOT_KEY" --header "Content-Type:text/plain;charset=UTF-8"
