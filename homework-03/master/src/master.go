package master

import (
	"context"
	"database/sql/driver"
	"fmt"
	"github.com/gin-gonic/gin"
	_ "github.com/go-redis/redis/v8"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	_ "github.com/lib/pq"
	"github.com/segmentio/kafka-go"
	"golang.org/x/crypto/ssh"
	"log"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"time"
)

func SetupRouter() *gin.Engine {
	r := gin.Default()

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "pong",
		})
	})

	return r
}

// struct for user json request
type create struct {
	LongUrl string `json:"longurl"`
}

type ViaSSHDialer struct {
	Client *ssh.Client
}

func (self *ViaSSHDialer) Open(s string) (_ driver.Conn, err error) {
	return pq.DialOpen(self, s)
}

func (self *ViaSSHDialer) Dial(network, address string) (net.Conn, error) {
	return self.Client.Dial(network, address)
}

func (self *ViaSSHDialer) DialTimeout(network, address string, timeout time.Duration) (net.Conn, error) {
	return self.Client.Dial(network, address)
}

func sendNewDataToReplicas(longLink, shortLink, topicToReplica string) error {
	writer := &kafka.Writer{
		Addr:  kafka.TCP("158.160.19.212:9092"),
		Topic: topicToReplica,
	}
	err := writer.WriteMessages(context.Background(),
		kafka.Message{
			Key:   []byte(shortLink), //longurl from db
			Value: []byte(longLink),  //tinyurl from replica
		})
	if err != nil {
		return fmt.Errorf("failed to write messages: %w", err)
	}

	if err = writer.Close(); err != nil {
		return fmt.Errorf("failed to close writer: %w", err)
	}

	return nil
}

func checkAndCreateTinyUrl(conn *sqlx.DB, linkBigFromUser string) (bool, string, error) {
	var nameOfSearchingLinkInDB string
	// проверяем есть ли ссылка в бд
	err := conn.QueryRow("SELECT longurl FROM links WHERE longurl=$1", linkBigFromUser).Scan(&nameOfSearchingLinkInDB)
	if err != nil {
		// если ссылки нет в бд
		return createTinyUrl(conn, linkBigFromUser)
	}

	//если ссылки есть в бд
	err = conn.QueryRow("SELECT tinyurl FROM links WHERE longurl=$1", linkBigFromUser).Scan(&nameOfSearchingLinkInDB)
	if err != nil {
		return false, "", fmt.Errorf("query select tinyurl from links: %w", err)
	}

	return false, nameOfSearchingLinkInDB, nil
}

func createTinyUrl(conn *sqlx.DB, linkBigFromUser string) (bool, string, error) {
	rand.Seed(time.Now().UnixNano())
	charsAlphabetAndNumbers := []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
		"abcdefghijklmnopqrstuvwxyz" +
		"0123456789")
	var buildTinyUrl strings.Builder
	for i := 0; i < 7; i++ {
		buildTinyUrl.WriteRune(charsAlphabetAndNumbers[rand.Intn(len(charsAlphabetAndNumbers))])
	} // делаем tinyUrl

	shortLinkForDB := buildTinyUrl.String()
	stmt, err := conn.Prepare("INSERT INTO links (longurl, tinyurl, count_click_on_link) VALUES ($1, $2, $3);") //помещяем ссылку в бд
	if err != nil {
		return false, "", fmt.Errorf("conn.Prepare insert tinyurl: %w", err)
	}
	defer stmt.Close()

	if _, err = stmt.Exec(linkBigFromUser, shortLinkForDB, 0); err != nil {
		return false, "", fmt.Errorf("stmt.Exec insert tinyurl: %w", err)
	}

	return true, shortLinkForDB, nil
}

func Put(ctx *gin.Context, conn *sqlx.DB) error {
	var (
		usersJSONLongUrl create //make new struct
	)
	if err := ctx.BindJSON(&usersJSONLongUrl); err != nil {
		return fmt.Errorf(" ctx.BindJSON: %v", err)
	}

	result, resultLink, err := checkAndCreateTinyUrl(conn, usersJSONLongUrl.LongUrl)
	if err != nil {
		return fmt.Errorf("checkAndCreateTinyUrl: %w", err)
	}

	if !result {
		ctx.JSON(http.StatusOK, gin.H{"longurl": usersJSONLongUrl.LongUrl, "tinyurl": resultLink}) // если в бд есть ссылка
		return nil
	}

	// если в бд нет ссылка
	err = sendNewDataToReplicas(usersJSONLongUrl.LongUrl, resultLink, "mdiagilev-test-master")
	if err != nil {
		return fmt.Errorf("sendNewDataToReplicas: %w", err)
	}
	ctx.JSON(http.StatusOK, gin.H{"longurl": usersJSONLongUrl.LongUrl, "tinyurl": resultLink})

	return nil
}

func MasterReadFromReplicaIncrClick(conn *sqlx.DB, topic string) {
	for {
		reader := kafka.NewReader(kafka.ReaderConfig{
			Brokers:   []string{"158.160.19.212:9092"},
			Topic:     topic,
			Partition: 0})
		reader.SetOffset(kafka.LastOffset)

		m, err := reader.ReadMessage(context.Background())
		if err != nil {
			fmt.Println("", err)
		}

		fmt.Printf("message with links from Master to replica: %s = %s\n", string(m.Key), string(m.Value)) //longurl, click
		// UPDATE totals
		//   SET total = total + 1
		//WHERE name = 'bill';
		//stmt, err := conn.Prepare("UPDATE links SET count_click_on_link = count_click_on_link + $1 WHERE longurl = $2 VALUES ($1, $2);", m.Value) //помещяем ссылку в бд
		//if err != nil {
		//	log.Fatal(err)
		//}
		//
		//_, err = stmt.Exec((m.Value), m.Key)
		//if err != nil {
		//	log.Fatal(err)
		//}
		//
		//defer stmt.Close()

		if err := reader.Close(); err != nil {
			log.Fatal("failed to close reader:", err)
		}

	}

}
