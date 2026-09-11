#!/bin/bash
set -e

echo "Building and starting all containers from scratch..."
docker compose up -d --build
echo "All containers are up!"
