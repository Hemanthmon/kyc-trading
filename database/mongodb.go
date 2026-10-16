package database

import (
	"context"
	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"log"
	"os"
)

func ConnectDB() *mongo.Client {

	err := godotenv.Load()

	if err != nil {
		log.Println("no .env file, using environment variables")
	}

	mongoURI := os.Getenv("MONGODB_URI")

	if mongoURI == "" {
		log.Fatal("Missing mongodb uri")
	}

	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI))

	if err != nil {
		log.Fatal("MongoDB connection failed:", err)
	}

	err = client.Ping(context.Background(), nil)

	if err != nil {
		log.Fatal("MongoDB ping failed:", err)
	}

	log.Println("Db connected successfully")

	return client
}
