package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/redis/go-redis/v9"
	"math"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var ctx = context.Background()
var rdb = redis.NewClient(&redis.Options{Addr: "172.17.0.2:6379"})

var roomsMu sync.Mutex
var rooms = make(map[string]*Room)
var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

// ==================================
// 구조체 정의
// ==================================
type Player struct {
	ID       string
	Conn     *websocket.Conn
	TimeLeft float64
	mu       sync.Mutex
}

type UnitInfo struct {
	UnitID int `json:"unitId"`
	Level  int `json:"level"`
}

type Monster struct {
	ID       int
	Type     string
	SubType  string
	HP       int
	MaxHP    int
	Path     []Pos
	Progress float64 // 0~1
	Escaped  bool
}

type Pos struct{ X, Y int }

type Room struct {
	ID          string
	A, B        *Player
	Wave        int
	MonstersA   map[int]*Monster
	MonstersB   map[int]*Monster
	monsterMu   sync.Mutex
	started     bool
	boardMutex  sync.Mutex
	pathCorners [4]Pos
}

// ==================================
// Room 메서드
// ==================================
func NewRoom(id string, a, b *Player) *Room {
	corners := [4]Pos{{0, 0}, {0, 5}, {5, 5}, {5, 0}}
	return &Room{
		ID:          id,
		A:           a,
		B:           b,
		Wave:        1,
		MonstersA:   make(map[int]*Monster),
		MonstersB:   make(map[int]*Monster),
		pathCorners: corners,
	}
}

func (r *Room) notifyAll(msg map[string]any) {
	if r.A != nil && r.A.Conn != nil {
		r.A.Conn.WriteJSON(msg)
	}
	if r.B != nil && r.B.Conn != nil {
		r.B.Conn.WriteJSON(msg)
	}
}

func (r *Room) notifyPlayer(player *Player, msg map[string]any) {
	if player != nil && player.Conn != nil {
		player.Conn.WriteJSON(msg)
	}
}

// 몬스터 경로 생성
func generatePath(corners [4]Pos) []Pos {
	path := []Pos{}
	for i := 0; i < len(corners); i++ {
		start := corners[i]
		end := corners[(i+1)%len(corners)]
		steps := 6
		for s := 0; s < steps; s++ {
			x := start.X + (end.X-start.X)*s/steps
			y := start.Y + (end.Y-start.Y)*s/steps
			path = append(path, Pos{x, y})
		}
	}
	return path
}

// 웨이브 몬스터 생성 및 브로드캐스트
func (r *Room) spawnWaveMonsters() {
	r.monsterMu.Lock()
	r.MonstersA = make(map[int]*Monster)
	r.MonstersB = make(map[int]*Monster)
	r.monsterMu.Unlock()

	count := 10 + (r.Wave-1)*3
	path := generatePath(r.pathCorners)

	for i := 0; i < count; i++ {
		idA := rand.Int()
		subtypeA := "BOOK"
		if rand.Float32() < 0.3 {
			subtypeA = "FOOD"
		}
		mA := &Monster{
			ID:       idA,
			Type:     "monster",
			SubType:  subtypeA,
			HP:       100,
			MaxHP:    100,
			Path:     path,
			Progress: 0,
			Escaped:  false,
		}

		idB := rand.Int()
		subtypeB := "BOOK"
		if rand.Float32() < 0.3 {
			subtypeB = "FOOD"
		}
		mB := &Monster{
			ID:       idB,
			Type:     "monster",
			SubType:  subtypeB,
			HP:       100,
			MaxHP:    100,
			Path:     path,
			Progress: 0,
			Escaped:  false,
		}

		r.monsterMu.Lock()
		r.MonstersA[idA] = mA
		r.MonstersB[idB] = mB
		r.monsterMu.Unlock()
	}

	// 브로드캐스트: 모든 플레이어에게 몬스터 리스트 전송
	mList := []map[string]any{}
	for _, m := range r.MonstersA {
		mList = append(mList, map[string]any{"id": m.ID, "type": m.Type, "subtype": m.SubType, "hp": m.HP})
	}
	for _, m := range r.MonstersB {
		mList = append(mList, map[string]any{"id": m.ID, "type": m.Type, "subtype": m.SubType, "hp": m.HP})
	}
	r.notifyAll(map[string]any{"type": "wave_start", "wave": r.Wave, "monsters": mList})
}

// 웨이브 시작 전 정보 및 카운트다운
func (r *Room) startCountdown() {
	go func() {
		// 먼저 웨이브 정보 전송
		r.notifyAll(map[string]any{"type": "wave_info", "wave": r.Wave})
		// 3초 대기
		time.Sleep(3 * time.Second)

		// 3-2-1 카운트다운
		for i := 3; i > 0; i-- {
			r.notifyAll(map[string]any{"type": "countdown", "count": i})
			time.Sleep(1 * time.Second)
		}

		// 카운트다운 완료 후 웨이브 몬스터 생성
		r.spawnWaveMonsters()
		// 게임 루프 시작
		r.runWaveLoop()
	}()
}

// 게임 시작
func (r *Room) runGameLoop() {
	r.A.TimeLeft = 300
	if r.B != nil {
		r.B.TimeLeft = 300
	}
	r.started = true
	r.startCountdown()
}

// 웨이브 루프
func (r *Room) runWaveLoop() {
	tick := 50 * time.Millisecond
	go func() {
		for (r.A != nil && r.A.TimeLeft > 0) || (r.B != nil && r.B.TimeLeft > 0) {
			time.Sleep(tick)
			dt := 0.05

			if r.A != nil {
				r.A.mu.Lock()
				r.A.TimeLeft -= dt
				if r.A.TimeLeft < 0 {
					r.A.TimeLeft = 0
				}
				r.A.mu.Unlock()
			}
			if r.B != nil {
				r.B.mu.Lock()
				r.B.TimeLeft -= dt
				if r.B.TimeLeft < 0 {
					r.B.TimeLeft = 0
				}
				r.B.mu.Unlock()
			}

			r.notifyAll(map[string]any{
				"type": "timer",
				"A": math.Round(func() float64 {
					if r.A != nil {
						return r.A.TimeLeft
					} else {
						return 0
					}
				}()*10) / 10,
				"B": math.Round(func() float64 {
					if r.B != nil {
						return r.B.TimeLeft
					} else {
						return 0
					}
				}()*10) / 10,
			})

			r.monsterMu.Lock()
			allDeadOrEscapedA := true
			posUpdateA := []map[string]any{}
			for _, m := range r.MonstersA {
				if m.Escaped || m.HP <= 0 {
					posUpdateA = append(posUpdateA, map[string]any{"id": m.ID, "hp": m.HP, "escaped": m.Escaped})
					continue
				}
				allDeadOrEscapedA = false
				m.Progress += 0.005
				if m.Progress > 1.0 {
					m.Progress = 1.0
				}
				idx := int(math.Floor(m.Progress * float64(len(m.Path)-1)))
				pos := m.Path[idx]
				posUpdateA = append(posUpdateA, map[string]any{"id": m.ID, "hp": m.HP, "x": pos.X, "y": pos.Y, "progress": math.Round(m.Progress*100) / 100})
				if m.Progress >= 1.0 && !m.Escaped {
					m.Escaped = true
					if r.A != nil {
						r.A.mu.Lock()
						r.A.TimeLeft -= 1
						if r.A.TimeLeft < 0 {
							r.A.TimeLeft = 0
						}
						r.A.mu.Unlock()
					}
				}
			}

			allDeadOrEscapedB := true
			posUpdateB := []map[string]any{}
			for _, m := range r.MonstersB {
				if m.Escaped || m.HP <= 0 {
					posUpdateB = append(posUpdateB, map[string]any{"id": m.ID, "hp": m.HP, "escaped": m.Escaped})
					continue
				}
				allDeadOrEscapedB = false
				m.Progress += 0.005
				if m.Progress > 1.0 {
					m.Progress = 1.0
				}
				idx := int(math.Floor(m.Progress * float64(len(m.Path)-1)))
				pos := m.Path[idx]
				posUpdateB = append(posUpdateB, map[string]any{"id": m.ID, "hp": m.HP, "x": pos.X, "y": pos.Y, "progress": math.Round(m.Progress*100) / 100})
				if m.Progress >= 1.0 && !m.Escaped {
					m.Escaped = true
					if r.B != nil {
						r.B.mu.Lock()
						r.B.TimeLeft -= 1
						if r.B.TimeLeft < 0 {
							r.B.TimeLeft = 0
						}
						r.B.mu.Unlock()
					}
				}
			}
			r.monsterMu.Unlock()

			// 브로드캐스트: 모든 플레이어에게 몬스터 위치 업데이트
			r.notifyAll(map[string]any{"type": "monster_positions", "positions": append(posUpdateA, posUpdateB...)})

			if allDeadOrEscapedA && allDeadOrEscapedB {
				r.Wave++
				r.startCountdown() // 다음 웨이브 시작 전 카운트다운
				return
			}
		}
		r.notifyAll(map[string]any{"type": "game_over"})
	}()
}

// ==================================
// 공격 처리
// ==================================
func (r *Room) handleAttack(playerId string, monsterId int, unitId int) {
	r.monsterMu.Lock()
	defer r.monsterMu.Unlock()
	var m *Monster
	var ok bool
	if r.A != nil && playerId == r.A.ID {
		m, ok = r.MonstersA[monsterId]
	} else if r.B != nil && playerId == r.B.ID {
		m, ok = r.MonstersB[monsterId]
	} else {
		return
	}
	if !ok || m.Escaped || m.HP <= 0 {
		return
	}
	damage := 10
	m.HP -= damage
	if m.HP < 0 {
		m.HP = 0
	}
	if m.HP == 0 {
		dop := 5 + rand.Intn(11)
		r.notifyAll(map[string]any{"type": "monster_dead", "monsterId": monsterId, "dopamin": dop})
	}
}

// ==================================
// 슬라이드 처리
// ==================================
func (r *Room) handleSlide(playerId string, board [][]UnitInfo) {
	key := fmt.Sprintf("game:%s:player:%s:board", r.ID, playerId)
	data, _ := json.Marshal(board)
	rdb.Set(ctx, key, data, 0)
	r.notifyAll(map[string]any{"type": "board_update", "playerId": playerId, "board": board})
}

// ==================================
// 소환/삭제 HTTP
// ==================================
func (r *Room) summonByHTTP(playerId string) (bool, map[string]any) {
	cost := 50
	dopKey := fmt.Sprintf("dopamin:%s", playerId)
	cur, _ := rdb.Get(ctx, dopKey).Int()
	if cur < cost {
		return false, map[string]any{"error": "not enough dopamin"}
	}
	rdb.DecrBy(ctx, dopKey, int64(cost))

	boardKey := fmt.Sprintf("game:%s:player:%s:board", r.ID, playerId)
	data, _ := rdb.Get(ctx, boardKey).Bytes()
	var board [][]UnitInfo
	json.Unmarshal(data, &board)
	placed := false
	for !placed {
		x := rand.Intn(6)
		y := rand.Intn(6)
		if board[y][x].UnitID == 0 {
			board[y][x] = UnitInfo{UnitID: rand.Intn(4) + 1, Level: 2}
			placed = true
		}
	}
	newData, _ := json.Marshal(board)
	rdb.Set(ctx, boardKey, newData, 0)
	r.notifyAll(map[string]any{"type": "summon_success", "playerId": playerId, "board": board})
	return true, map[string]any{"success": true, "board": board}
}

func (r *Room) deleteByHTTP(playerId string, x, y int) (bool, map[string]any) {
	boardKey := fmt.Sprintf("game:%s:player:%s:board", r.ID, playerId)
	data, _ := rdb.Get(ctx, boardKey).Bytes()
	var board [][]UnitInfo
	json.Unmarshal(data, &board)
	if board[y][x].UnitID == 0 {
		return false, map[string]any{"error": "empty"}
	}
	board[y][x] = UnitInfo{}
	newData, _ := json.Marshal(board)
	rdb.Set(ctx, boardKey, newData, 0)
	if r.A != nil && playerId == r.A.ID {
		r.A.mu.Lock()
		r.A.TimeLeft -= 2
		if r.A.TimeLeft < 0 {
			r.A.TimeLeft = 0
		}
		r.A.mu.Unlock()
	}
	if r.B != nil && playerId == r.B.ID {
		r.B.mu.Lock()
		r.B.TimeLeft -= 2
		if r.B.TimeLeft < 0 {
			r.B.TimeLeft = 0
		}
		r.B.mu.Unlock()
	}
	r.notifyAll(map[string]any{"type": "delete", "playerId": playerId, "board": board})
	return true, map[string]any{"success": true, "board": board}
}

func wsHandler(c *gin.Context) {
	roomId := c.Query("roomId")
	playerId := c.Query("playerId")
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	player := &Player{ID: playerId, Conn: conn, TimeLeft: 60}

	roomsMu.Lock()
	r, ok := rooms[roomId]
	if !ok {
		r = NewRoom(roomId, player, nil)
		rooms[roomId] = r
	} else {
		if r.A == nil {
			r.A = player
		} else {
			r.B = player
		}
	}
	roomsMu.Unlock()

	// 두 플레이어 준비 완료 시 게임 시작
	if r.A != nil && r.B != nil && !r.started {
		r.runGameLoop()
	}

	defer func() {
		conn.Close()
		roomsMu.Lock()
		if r.A != nil && r.A.ID == playerId {
			r.A = nil
		}
		if r.B != nil && r.B.ID == playerId {
			r.B = nil
		}
		// 방 삭제는 두 플레이어 모두 없을 때만
		if r.A == nil && r.B == nil {
			delete(rooms, roomId)
		}
		roomsMu.Unlock()
	}()

	for {
		_, msgData, err := conn.ReadMessage()
		if err != nil {
			fmt.Println("WS disconnect", playerId)
			break
		}
		var msg map[string]any
		if err := json.Unmarshal(msgData, &msg); err != nil {
			continue
		}

		switch msg["type"] {
		case "attack":
			monsterId := int(msg["monsterId"].(float64))
			unitId := int(msg["unitId"].(float64))
			r.handleAttack(playerId, monsterId, unitId)
		case "slide":
			boardData, _ := json.Marshal(msg["board"])
			var board [][]UnitInfo
			json.Unmarshal(boardData, &board)
			r.handleSlide(playerId, board)
		}
	}
}

func initPlayerBoard(roomId, playerId string) {
	board := make([][]UnitInfo, 6)
	for i := range board {
		board[i] = make([]UnitInfo, 6)
	}
	units := []int{1, 2, 3, 4}
	for _, u := range units {
		for {
			x, y := rand.Intn(6), rand.Intn(6)
			if board[y][x].UnitID == 0 {
				board[y][x] = UnitInfo{UnitID: u, Level: 2}
				break
			}
		}
	}
	key := fmt.Sprintf("game:%s:player:%s:board", roomId, playerId)
	data, _ := json.Marshal(board)
	rdb.Set(ctx, key, data, 0)
	rdb.Set(ctx, fmt.Sprintf("dopamin:%s", playerId), 1000, 0)
}

func healthHandler(c *gin.Context) {
	c.JSON(200, "OK")
}

func main() {
	rand.Seed(time.Now().UnixNano())
	router := gin.Default()
	router.POST("/summon/:roomId", func(c *gin.Context) {
		roomId := c.Param("roomId")
		var body struct {
			PlayerId string `json:"playerId"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": "bad"})
			return
		}
		roomsMu.Lock()
		r, ok := rooms[roomId]
		roomsMu.Unlock()
		if !ok {
			c.JSON(404, gin.H{"error": "room not found"})
			return
		}
		_, resp := r.summonByHTTP(body.PlayerId) // ok2 제거하고 _ 사용
		c.JSON(200, resp)
	})

	router.POST("/delete/:roomId", func(c *gin.Context) {
		roomId := c.Param("roomId")
		var body struct {
			PlayerId string `json:"playerId"`
			X        int
			Y        int
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": "bad"})
			return
		}
		roomsMu.Lock()
		r, ok := rooms[roomId]
		roomsMu.Unlock()
		if !ok {
			c.JSON(404, gin.H{"error": "room not found"})
			return
		}
		_, resp := r.deleteByHTTP(body.PlayerId, body.X, body.Y) // ok2 제거하고 _ 사용
		c.JSON(200, resp)
	})
	router.GET("/ws", wsHandler)
	router.GET("/health", healthHandler)

	router.GET("/flushall", func(c *gin.Context) {
		if err := rdb.FlushAll(ctx).Err(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"ok": true, "message": "all redis data flushed"})
	})

	// 예제 방 및 플레이어 초기화
	roomId := "room1"
	initPlayerBoard(roomId, "A")
	initPlayerBoard(roomId, "B")

	fmt.Println("Server start :8080")
	router.Run("0.0.0.0:8080")
}
