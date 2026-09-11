#!/bin/bash
set -e

echo "Pausing all containers..."
docker compose stop
echo "All containers paused!"
