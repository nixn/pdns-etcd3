#!/bin/sh
set -e
simpleproxy -L 2379 -R "$1" &
./etcdkeeper
