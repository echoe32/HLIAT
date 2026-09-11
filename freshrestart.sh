#!/bin/bash
set -e

echo "Stopping and removing all containers..."
docker compose down
echo "All containers removed!"
