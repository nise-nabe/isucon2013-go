#!/bin/bash
BASE=$(cd $(dirname $0); pwd)

mysql -u isucon isucon < $BASE/alter.sql
mkdir -p /dev/shm/gorilla/
chmod 777 /dev/shm/gorilla/
