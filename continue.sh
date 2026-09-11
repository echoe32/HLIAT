#!/bin/bash
set -e

echo "Resuming all containers..."
docker compose start
echo "All containers are back up!"
