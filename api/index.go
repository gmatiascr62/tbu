package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ContextUserKey = "auth_user"

var (
	authClientOnce sync.Once
	authClientInst *Client
	once           sync.Once
	router         *gin.Engine
	pool           *pgxpool.Pool
	err            error
)

// config
type Config struct {
	SupabaseURL     string
	SupabaseAnonKey string
	DatabaseURL     string
}

func Load() Config {
	return Config{
		SupabaseURL:     os.Getenv("SUPABASE_URL"),
		SupabaseAnonKey: os.Getenv("SUPABASE_ANON_KEY"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
	}
}

type createMessageBody struct {
	Content string `json:"content"`
}
type createFriendRequestBody struct {
	UserID string `json:"user_id"`
}
type setupProfileRequest struct {
	Username      string `json:"username"`
	AddressNumber int    `json:"address_number"`
}

// models de db
type AuthUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}
type FriendRequestItem struct {
	ID        string    `json:"id"`
	FromID    string    `json:"from_user_id"`
	Username  string    `json:"username"`
	AvatarURL *string   `json:"avatar_url"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}
type FriendItem struct {
	ID        string  `json:"id"`
	Username  string  `json:"username"`
	AvatarURL *string `json:"avatar_url"`
}
type MessageItem struct {
	ID         string     `json:"id"`
	FromUserID string     `json:"from_user_id"`
	ToUserID   string     `json:"to_user_id"`
	Content    string     `json:"content"`
	CreatedAt  time.Time  `json:"created_at"`
	ReadAt     *time.Time `json:"read_at"`
}
type Profile struct {
	ID            string    `json:"id"`
	Username      string    `json:"username"`
	AvatarURL     *string   `json:"avatar_url"`
	Bio           *string   `json:"bio"`
	AddressNumber *int      `json:"address_number,omitempty"`
	StreetNumber  *int      `json:"street_number,omitempty"`
	LotNumber     *int      `json:"lot_number,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
type SupabaseUser struct {
	ID               string         `json:"id"`
	Email            string         `json:"email"`
	Role             string         `json:"role"`
	Aud              string         `json:"aud"`
	AppMetadata      map[string]any `json:"app_metadata"`
	UserMetadata     map[string]any `json:"user_metadata"`
	EmailConfirmedAt string         `json:"email_confirmed_at"`
	Phone            string         `json:"phone"`
	ConfirmedAt      string         `json:"confirmed_at"`
	LastSignInAt     string         `json:"last_sign_in_at"`
	CreatedAt        string         `json:"created_at"`
	UpdatedAt        string         `json:"updated_at"`
}
type UnreadCountItem struct {
	UserID      string `json:"user_id"`
	UnreadCount int    `json:"unread_count"`
}
type UserListItem struct {
	ID                 string  `json:"id"`
	Username           string  `json:"username"`
	AvatarURL          *string `json:"avatar_url"`
	RelationshipStatus string  `json:"relationship_status"`
	AddressNumber      *int    `json:"address_number,omitempty"`
	StreetNumber       *int    `json:"street_number,omitempty"`
	LotNumber          *int    `json:"lot_number,omitempty"`
}

// reposotory para db
type FriendRepository struct {
	db *pgxpool.Pool
}

func NewFriendRepository(db *pgxpool.Pool) *FriendRepository {
	return &FriendRepository{db: db}
}
func (r *FriendRepository) ListByUserID(ctx context.Context, userID string) ([]FriendItem, error) {
	query := `
		select
			p.id,
			p.username,
			p.avatar_url
		from public.friends f
		join public.profiles p
			on p.id = case
				when f.user_low = $1 then f.user_high
				else f.user_low
			end
		where f.user_low = $1 or f.user_high = $1
		order by p.username_lower asc
	`

	rows, err := r.db.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	friends := make([]FriendItem, 0)

	for rows.Next() {
		var item FriendItem
		if err := rows.Scan(
			&item.ID,
			&item.Username,
			&item.AvatarURL,
		); err != nil {
			return nil, err
		}
		friends = append(friends, item)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return friends, nil
}

var ErrCannotFriendSelf = errors.New("cannot send friend request to self")
var ErrTargetProfileNotFound = errors.New("target profile not found")
var ErrAlreadyFriends = errors.New("already friends")
var ErrPendingRequestAlreadyExists = errors.New("pending friend request already exists")
var ErrFriendRequestNotFound = errors.New("friend request not found")

type FriendRequestRepository struct {
	db *pgxpool.Pool
}

func NewFriendRequestRepository(db *pgxpool.Pool) *FriendRequestRepository {
	return &FriendRequestRepository{db: db}
}
func orderedPair(a, b string) (string, string) {
	if strings.Compare(a, b) < 0 {
		return a, b
	}
	return b, a
}
func (r *FriendRequestRepository) Create(ctx context.Context, fromUserID, toUserID string) (string, error) {
	if fromUserID == toUserID {
		return "", ErrCannotFriendSelf
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	// Verificar que exista el perfil destino
	var exists bool
	err = tx.QueryRow(ctx, `
		select exists(
			select 1
			from public.profiles
			where id = $1
		)
	`, toUserID).Scan(&exists)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", ErrTargetProfileNotFound
	}

	low, high := orderedPair(fromUserID, toUserID)

	// Verificar si ya son amigos
	err = tx.QueryRow(ctx, `
		select exists(
			select 1
			from public.friends
			where user_low = $1 and user_high = $2
		)
	`, low, high).Scan(&exists)
	if err != nil {
		return "", err
	}
	if exists {
		return "", ErrAlreadyFriends
	}

	// Insertar solicitud pendiente
	var requestID string
	err = tx.QueryRow(ctx, `
		insert into public.friend_requests (
			from_user_id,
			to_user_id,
			pair_user_low,
			pair_user_high,
			status
		)
		values ($1, $2, $3, $4, 'pending')
		returning id
	`, fromUserID, toUserID, low, high).Scan(&requestID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			// unique index parcial de pending
			if pgErr.Code == "23505" {
				return "", ErrPendingRequestAlreadyExists
			}
		}
		return "", err
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}

	return requestID, nil
}
func (r *FriendRequestRepository) ListReceived(ctx context.Context, userID string) ([]FriendRequestItem, error) {

	query := `
		select
			fr.id,
			fr.from_user_id,
			p.username,
			p.avatar_url,
			fr.status,
			fr.created_at
		from public.friend_requests fr
		join public.profiles p
			on p.id = fr.from_user_id
		where fr.to_user_id = $1
		  and fr.status = 'pending'
		order by fr.created_at desc
	`

	rows, err := r.db.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []FriendRequestItem

	for rows.Next() {
		var item FriendRequestItem

		err := rows.Scan(
			&item.ID,
			&item.FromID,
			&item.Username,
			&item.AvatarURL,
			&item.Status,
			&item.CreatedAt,
		)

		if err != nil {
			return nil, err
		}

		requests = append(requests, item)
	}

	return requests, nil
}
func (r *FriendRequestRepository) Accept(ctx context.Context, requestID, currentUserID string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var fromUserID, toUserID, status string

	err = tx.QueryRow(ctx, `
		select from_user_id, to_user_id, status
		from public.friend_requests
		where id = $1
		for update
	`, requestID).Scan(&fromUserID, &toUserID, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrFriendRequestNotFound
		}
		return err
	}

	if toUserID != currentUserID {
		return errors.New("not allowed to accept this request")
	}

	if status != "pending" {
		return errors.New("request already processed")
	}

	low, high := orderedPair(fromUserID, toUserID)

	var alreadyFriends bool
	err = tx.QueryRow(ctx, `
		select exists(
			select 1
			from public.friends
			where user_low = $1 and user_high = $2
		)
	`, low, high).Scan(&alreadyFriends)
	if err != nil {
		return err
	}
	if alreadyFriends {
		return ErrAlreadyFriends
	}

	_, err = tx.Exec(ctx, `
		update public.friend_requests
		set status = 'accepted',
		    responded_at = now()
		where id = $1
	`, requestID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		insert into public.friends (user_low, user_high)
		values ($1, $2)
	`, low, high)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

var ErrNotFriends = errors.New("users are not friends")

type MessageRepository struct {
	db *pgxpool.Pool
}

func NewMessageRepository(db *pgxpool.Pool) *MessageRepository {
	return &MessageRepository{db: db}
}
func orderedConversation(a, b string) (string, string) {
	if strings.Compare(a, b) < 0 {
		return a, b
	}
	return b, a
}
func (r *MessageRepository) areFriends(ctx context.Context, userA, userB string) (bool, error) {
	low, high := orderedConversation(userA, userB)

	var exists bool
	err := r.db.QueryRow(ctx, `
		select exists(
			select 1
			from public.friends
			where user_low = $1 and user_high = $2
		)
	`, low, high).Scan(&exists)
	if err != nil {
		return false, err
	}

	return exists, nil
}
func (r *MessageRepository) Create(ctx context.Context, fromUserID, toUserID, content string) (*MessageItem, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, errors.New("empty_content")
	}

	ok, err := r.areFriends(ctx, fromUserID, toUserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFriends
	}

	low, high := orderedConversation(fromUserID, toUserID)

	query := `
		insert into public.messages (
			from_user_id,
			to_user_id,
			conversation_low,
			conversation_high,
			content
		)
		values ($1, $2, $3, $4, $5)
		returning id, from_user_id, to_user_id, content, created_at, read_at
	`

	var msg MessageItem
	err = r.db.QueryRow(ctx, query, fromUserID, toUserID, low, high, content).Scan(
		&msg.ID,
		&msg.FromUserID,
		&msg.ToUserID,
		&msg.Content,
		&msg.CreatedAt,
		&msg.ReadAt,
	)
	if err != nil {
		return nil, err
	}

	return &msg, nil
}
func (r *MessageRepository) ListConversation(ctx context.Context, currentUserID, friendID string, limit, offset int) ([]MessageItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	ok, err := r.areFriends(ctx, currentUserID, friendID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFriends
	}

	low, high := orderedConversation(currentUserID, friendID)

	query := `
		select
			id,
			from_user_id,
			to_user_id,
			content,
			created_at,
			read_at
		from public.messages
		where conversation_low = $1
		  and conversation_high = $2
		order by created_at desc
		limit $3 offset $4
	`

	rows, err := r.db.Query(ctx, query, low, high, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]MessageItem, 0)

	for rows.Next() {
		var msg MessageItem
		if err := rows.Scan(
			&msg.ID,
			&msg.FromUserID,
			&msg.ToUserID,
			&msg.Content,
			&msg.CreatedAt,
			&msg.ReadAt,
		); err != nil {
			return nil, err
		}
		items = append(items, msg)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return items, nil
}
func (r *MessageRepository) MarkAsRead(ctx context.Context, currentUserID, friendID string) (int64, error) {
	ok, err := r.areFriends(ctx, currentUserID, friendID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrNotFriends
	}

	tag, err := r.db.Exec(ctx, `
		update public.messages
		set read_at = now()
		where from_user_id = $1
		  and to_user_id = $2
		  and read_at is null
	`, friendID, currentUserID)
	if err != nil {
		return 0, err
	}

	return tag.RowsAffected(), nil
}
func (r *MessageRepository) GetUnreadCounts(ctx context.Context, currentUserID string) ([]UnreadCountItem, error) {
	query := `
		select
			from_user_id as user_id,
			count(*)::int as unread_count
		from public.messages
		where to_user_id = $1
		  and read_at is null
		group by from_user_id
		order by count(*) desc
	`

	rows, err := r.db.Query(ctx, query, currentUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]UnreadCountItem, 0)

	for rows.Next() {
		var item UnreadCountItem
		if err := rows.Scan(&item.UserID, &item.UnreadCount); err != nil {
			return nil, err
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return items, nil
}

var ErrProfileNotFound = errors.New("profile not found")
var ErrUsernameTaken = errors.New("username already taken")
var ErrProfileAlreadyExists = errors.New("profile already exists")
var ErrAddressTaken = errors.New("address already taken")

type ProfileRepository struct {
	db *pgxpool.Pool
}

func NewProfileRepository(db *pgxpool.Pool) *ProfileRepository {
	return &ProfileRepository{db: db}
}

func decodeAddressNumber(addressNumber int) (int, int) {
	street := addressNumber / 100
	lot := addressNumber % 100
	return street, lot
}

func (r *ProfileRepository) Create(ctx context.Context, userID, username string, addressNumber int) (*Profile, error) {
	username = strings.TrimSpace(username)
	usernameLower := strings.ToLower(username)

	query := `
		insert into public.profiles (id, username, username_lower, address_number)
		values ($1, $2, $3, $4)
		returning id, username, avatar_url, bio, address_number, created_at, updated_at
	`

	var p Profile
	err := r.db.QueryRow(ctx, query, userID, username, usernameLower, addressNumber).Scan(
		&p.ID,
		&p.Username,
		&p.AvatarURL,
		&p.Bio,
		&p.AddressNumber,
		&p.CreatedAt,
		&p.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			if pgErr.Code == "23505" {
				if strings.Contains(pgErr.ConstraintName, "profiles_pkey") {
					return nil, ErrProfileAlreadyExists
				}
				if strings.Contains(pgErr.ConstraintName, "address_number") {
					return nil, ErrAddressTaken
				}
				return nil, ErrUsernameTaken
			}
		}
		return nil, err
	}

	if p.AddressNumber != nil {
		street, lot := decodeAddressNumber(*p.AddressNumber)
		p.StreetNumber = &street
		p.LotNumber = &lot
	}

	return &p, nil
}
func (r *ProfileRepository) GetByID(ctx context.Context, userID string) (*Profile, error) {
	query := `
		select id, username, avatar_url, bio, address_number, created_at, updated_at
		from public.profiles
		where id = $1
	`

	var p Profile
	err := r.db.QueryRow(ctx, query, userID).Scan(
		&p.ID,
		&p.Username,
		&p.AvatarURL,
		&p.Bio,
		&p.AddressNumber,
		&p.CreatedAt,
		&p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProfileNotFound
		}
		return nil, err
	}

	if p.AddressNumber != nil {
		street, lot := decodeAddressNumber(*p.AddressNumber)
		p.StreetNumber = &street
		p.LotNumber = &lot
	}

	return &p, nil
}
func (r *ProfileRepository) ListUsers(ctx context.Context, currentUserID string, limit, offset int) ([]UserListItem, error) {
	if limit <= 0 || limit > 999 {
		limit = 999
	}
	if offset < 0 {
		offset = 0
	}

	query := `
		select
			p.id,
			p.username,
			p.avatar_url,
			case
				when f.user_low is not null then 'friend'
				when fr_sent.id is not null then 'request_sent'
				when fr_received.id is not null then 'request_received'
				else 'none'
			end as relationship_status,
			p.address_number
		from public.profiles p
		left join public.friends f
			on (
				((f.user_low = $1 and f.user_high = p.id) or (f.user_high = $1 and f.user_low = p.id))
			)
		left join public.friend_requests fr_sent
			on fr_sent.from_user_id = $1
			and fr_sent.to_user_id = p.id
			and fr_sent.status = 'pending'
		left join public.friend_requests fr_received
			on fr_received.from_user_id = p.id
			and fr_received.to_user_id = $1
			and fr_received.status = 'pending'
		order by p.address_number asc nulls last, p.username_lower asc
		limit $2 offset $3
	`

	rows, err := r.db.Query(ctx, query, currentUserID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []UserListItem
	for rows.Next() {
		var u UserListItem
		if err := rows.Scan(
			&u.ID,
			&u.Username,
			&u.AvatarURL,
			&u.RelationshipStatus,
			&u.AddressNumber,
		); err != nil {
			return nil, err
		}
		if u.AddressNumber != nil {
			street, lot := decodeAddressNumber(*u.AddressNumber)
			u.StreetNumber = &street
			u.LotNumber = &lot
		}
		users = append(users, u)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return users, nil
}

type Client struct {
	baseURL    string
	anonKey    string
	httpClient *http.Client
}

func NewClient(cfg Config) *Client {
	return &Client{
		baseURL: strings.TrimRight(cfg.SupabaseURL, "/"),
		anonKey: cfg.SupabaseAnonKey,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}
func (c *Client) GetUser(accessToken string) (*SupabaseUser, int, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/auth/v1/user", nil)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("crear request auth: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("apikey", c.anonKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("consultar supabase auth: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, nil
	}

	var user SupabaseUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("decodear usuario auth: %w", err)
	}

	return &user, http.StatusOK, nil
}

func GetPool() (*pgxpool.Pool, error) {
	once.Do(func() {

		cfg := Load()

		if cfg.DatabaseURL == "" {
			err = fmt.Errorf("DATABASE_URL no configurada")
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		fmt.Println("Parseando DATABASE_URL...")

		poolConfig, parseErr := pgxpool.ParseConfig(cfg.DatabaseURL)
		if parseErr != nil {
			fmt.Println("ERROR parse DATABASE_URL:", parseErr)
			err = fmt.Errorf("parse DATABASE_URL: %w", parseErr)
			return
		}

		poolConfig.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

		poolConfig.MaxConns = 5
		poolConfig.MinConns = 0

		fmt.Println("Creando pool...")

		pool, err = pgxpool.NewWithConfig(ctx, poolConfig)
		if err != nil {
			fmt.Println("ERROR crear pool:", err)
			err = fmt.Errorf("crear pool pgx: %w", err)
			return
		}

		fmt.Println("Haciendo ping a la ..")

		if pingErr := pool.Ping(ctx); pingErr != nil {
			pool.Close()
			pool = nil
			err = fmt.Errorf("ping db: %w", pingErr)
			return
		}

		fmt.Println("Conexión a DB exitosa")
	})

	return pool, err
}
func getAuthClient() *Client {
	authClientOnce.Do(func() {
		cfg := Load()
		authClientInst = NewClient(cfg)
	})
	return authClientInst
}
func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {

		authHeader := c.GetHeader("Authorization")

		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"error":   "missing_authorization_header",
				"message": "Falta el header Authorization",
			})
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)

		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"error":   "invalid_authorization_header",
				"message": "El header Authorization debe ser Bearer <token>",
			})
			c.Abort()
			return
		}

		token := strings.TrimSpace(parts[1])

		client := getAuthClient()

		userData, statusCode, err := client.GetUser(token)

		if err != nil {
			c.JSON(statusCode, gin.H{
				"success": false,
				"error":   "auth_verification_failed",
				"message": "No se pudo verificar el token con Supabase",
			})
			c.Abort()
			return
		}

		if userData == nil || userData.ID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"error":   "invalid_token",
				"message": "Token inválido o expirado",
			})
			c.Abort()
			return
		}

		user := AuthUser{
			ID:    userData.ID,
			Email: userData.Email,
			Role:  userData.Role,
		}

		c.Set(ContextUserKey, user)

		c.Next()
	}
}

// handlers
func Me(c *gin.Context) {
	rawUser, exists := c.Get(ContextUserKey)
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"error":   "unauthorized",
			"message": "Usuario no autenticado",
		})
		return
	}

	user, ok := rawUser.(AuthUser)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "invalid_auth_context",
			"message": "Contexto de autenticación inválido",
		})
		return
	}


	pool, err := GetPool()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "db_unavailable",
			"message": "No se pudo conectar a la base de datos",
		})
		return
	}

	repo := NewProfileRepository(pool)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	profile, err := repo.GetByID(ctx, user.ID)
	if err != nil {
		if errors.Is(err, ErrProfileNotFound) {
			c.JSON(http.StatusOK, gin.H{
				"success": true,
				"data": gin.H{
					"id":             user.ID,
					"email":          user.Email,
					"role":           user.Role,
					"profile_exists": false,
				},
			})
			return
		}

		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "profile_fetch_failed",
			"message": "No se pudo obtener el perfil",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"id":             user.ID,
			"email":          user.Email,
			"role":           user.Role,
			"profile_exists": true,
			"profile":        profile,
		},
	})
}
func AcceptFriendRequest(c *gin.Context) {
	rawUser, exists := c.Get(ContextUserKey)
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"error":   "unauthorized",
			"message": "Usuario no autenticado",
		})
		return
	}

	user, ok := rawUser.(AuthUser)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "invalid_auth_context",
			"message": "Contexto de autenticación inválido",
		})
		return
	}

	requestID := c.Param("id")
	if requestID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "missing_request_id",
			"message": "Falta el id de la solicitud",
		})
		return
	}

	pool, err := GetPool()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "db_unavailable",
			"message": "No se pudo conectar a la base de datos",
		})
		return
	}

	repo := NewFriendRequestRepository(pool)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	err = repo.Accept(ctx, requestID, user.ID)
	if err != nil {
		switch err.Error() {
		case "not allowed to accept this request":
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"error":   "not_allowed",
				"message": "No podés aceptar esta solicitud",
			})
			return
		case "request already processed":
			c.JSON(http.StatusConflict, gin.H{
				"success": false,
				"error":   "request_already_processed",
				"message": "La solicitud ya fue procesada",
			})
			return
		default:
			if err == ErrAlreadyFriends {
				c.JSON(http.StatusConflict, gin.H{
					"success": false,
					"error":   "already_friends",
					"message": "Ya son amigos",
				})
				return
			}

			if err != nil {
				if errors.Is(err, ErrFriendRequestNotFound) {
					c.JSON(http.StatusNotFound, gin.H{
						"success": false,
						"error":   "friend_request_not_found",
						"message": "La solicitud de amistad no existe",
					})
					return
				}

				if errors.Is(err, ErrTargetProfileNotFound) {
					c.JSON(http.StatusNotFound, gin.H{
						"success": false,
						"error":   "target_profile_not_found",
						"message": "Perfil destino no encontrado",
					})
					return
				}

				c.JSON(http.StatusInternalServerError, gin.H{
					"success": false,
					"error":   "accept_friend_request_failed",
					"message": "No se pudo aceptar la solicitud",
				})
				return
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"status": "accepted",
		},
	})
}
func ListReceivedFriendRequests(c *gin.Context) {

	rawUser, exists := c.Get(ContextUserKey)
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
		})
		return
	}

	user := rawUser.(AuthUser)

	pool, err := GetPool()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
		})
		return
	}

	repo := NewFriendRequestRepository(pool)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	requests, err := repo.ListReceived(ctx, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    requests,
	})
}
func CreateFriendRequest(c *gin.Context) {
	rawUser, exists := c.Get(ContextUserKey)
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"error":   "unauthorized",
			"message": "Usuario no autenticado",
		})
		return
	}

	user, ok := rawUser.(AuthUser)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "invalid_auth_context",
			"message": "Contexto de autenticación inválido",
		})
		return
	}

	var body createFriendRequestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "invalid_body",
			"message": "JSON inválido",
		})
		return
	}

	body.UserID = strings.TrimSpace(body.UserID)
	if body.UserID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "missing_user_id",
			"message": "Falta user_id",
		})
		return
	}

	pool, err := GetPool()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "db_unavailable",
			"message": "No se pudo conectar a la base de datos",
		})
		return
	}

	repo := NewFriendRequestRepository(pool)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	requestID, err := repo.Create(ctx, user.ID, body.UserID)
	if err != nil {
		switch err {
		case ErrCannotFriendSelf:
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "cannot_friend_self",
				"message": "No podés enviarte una solicitud a vos mismo",
			})
			return
		case ErrTargetProfileNotFound:
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"error":   "target_profile_not_found",
				"message": "El usuario destino no existe o no tiene perfil",
			})
			return
		case ErrAlreadyFriends:
			c.JSON(http.StatusConflict, gin.H{
				"success": false,
				"error":   "already_friends",
				"message": "Ya son amigos",
			})
			return
		case ErrPendingRequestAlreadyExists:
			c.JSON(http.StatusConflict, gin.H{
				"success": false,
				"error":   "pending_request_exists",
				"message": "Ya existe una solicitud pendiente entre ambos usuarios",
			})
			return
		default:
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"error":   "friend_request_create_failed",
				"message": "No se pudo crear la solicitud de amistad",
			})
			return
		}
	}

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"data": gin.H{
			"id":     requestID,
			"status": "pending",
		},
	})
}
func ListFriends(c *gin.Context) {
	rawUser, exists := c.Get(ContextUserKey)
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"error":   "unauthorized",
			"message": "Usuario no autenticado",
		})
		return
	}

	user, ok := rawUser.(AuthUser)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "invalid_auth_context",
			"message": "Contexto de autenticación inválido",
		})
		return
	}

	pool, err := GetPool()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "db_unavailable",
			"message": "No se pudo conectar a la base de datos",
		})
		return
	}

	repo := NewFriendRepository(pool)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	friends, err := repo.ListByUserID(ctx, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "friends_list_failed",
			"message": "No se pudo obtener la lista de amigos",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    friends,
	})
}
func Health(c *gin.Context) {
	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"ok": true,
		},
	})
}
func MarkChatAsRead(c *gin.Context) {
	rawUser, exists := c.Get(ContextUserKey)
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"error":   "unauthorized",
			"message": "Usuario no autenticado",
		})
		return
	}

	user, ok := rawUser.(AuthUser)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "invalid_auth_context",
			"message": "Contexto de autenticación inválido",
		})
		return
	}

	friendID := strings.TrimSpace(c.Param("friendId"))
	if friendID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "missing_friend_id",
			"message": "Falta friendId",
		})
		return
	}

	pool, err := GetPool()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "db_unavailable",
			"message": "No se pudo conectar a la base de datos",
		})
		return
	}

	repo := NewMessageRepository(pool)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	count, err := repo.MarkAsRead(ctx, user.ID, friendID)
	if err != nil {
		if err == ErrNotFriends {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"error":   "not_friends",
				"message": "Solo podés marcar mensajes de amigos",
			})
			return
		}

		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "mark_read_failed",
			"message": "No se pudieron marcar los mensajes como leídos",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"marked_count": count,
		},
	})
}
func CreateMessage(c *gin.Context) {
	rawUser, exists := c.Get(ContextUserKey)
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"error":   "unauthorized",
			"message": "Usuario no autenticado",
		})
		return
	}

	user, ok := rawUser.(AuthUser)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "invalid_auth_context",
			"message": "Contexto de autenticación inválido",
		})
		return
	}

	friendID := strings.TrimSpace(c.Param("friendId"))
	if friendID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "missing_friend_id",
			"message": "Falta friendId",
		})
		return
	}

	var body createMessageBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "invalid_body",
			"message": "JSON inválido",
		})
		return
	}

	body.Content = strings.TrimSpace(body.Content)
	if body.Content == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "empty_content",
			"message": "El mensaje no puede estar vacío",
		})
		return
	}

	pool, err := GetPool()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "db_unavailable",
			"message": "No se pudo conectar a la base de datos",
		})
		return
	}

	repo := NewMessageRepository(pool)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	msg, err := repo.Create(ctx, user.ID, friendID, body.Content)
	if err != nil {
		if err == ErrNotFriends {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"error":   "not_friends",
				"message": "Solo podés enviar mensajes a amigos",
			})
			return
		}
		if err.Error() == "empty_content" {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "empty_content",
				"message": "El mensaje no puede estar vacío",
			})
			return
		}

		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "message_create_failed",
			"message": "No se pudo enviar el mensaje",
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"data":    msg,
	})
}
func ListMessages(c *gin.Context) {
	rawUser, exists := c.Get(ContextUserKey)
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"error":   "unauthorized",
			"message": "Usuario no autenticado",
		})
		return
	}

	user, ok := rawUser.(AuthUser)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "invalid_auth_context",
			"message": "Contexto de autenticación inválido",
		})
		return
	}

	friendID := strings.TrimSpace(c.Param("friendId"))
	if friendID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "missing_friend_id",
			"message": "Falta friendId",
		})
		return
	}

	limit := 50
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "invalid_limit",
				"message": "limit inválido",
			})
			return
		}
		limit = parsed
	}

	offset := 0
	if raw := c.Query("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "invalid_offset",
				"message": "offset inválido",
			})
			return
		}
		offset = parsed
	}

	pool, err := GetPool()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "db_unavailable",
			"message": "No se pudo conectar a la base de datos",
		})
		return
	}

	repo := NewMessageRepository(pool)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	items, err := repo.ListConversation(ctx, user.ID, friendID, limit, offset)
	if err != nil {
		if err == ErrNotFriends {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"error":   "not_friends",
				"message": "Solo podés ver mensajes con amigos",
			})
			return
		}

		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "messages_list_failed",
			"message": "No se pudo obtener el historial",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    items,
	})
}
func ProfileSetup(c *gin.Context) {
	rawUser, exists := c.Get(ContextUserKey)
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"error":   "unauthorized",
			"message": "Usuario no autenticado",
		})
		return
	}

	user, ok := rawUser.(AuthUser)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "invalid_auth_context",
			"message": "Contexto de autenticación inválido",
		})
		return
	}

	var req setupProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "invalid_body",
			"message": "JSON inválido",
		})
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) < 3 || len(req.Username) > 30 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "invalid_username",
			"message": "El username debe tener entre 3 y 30 caracteres",
		})
		return
	}

	if req.AddressNumber < 101 || req.AddressNumber > 999 || req.AddressNumber%100 == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "invalid_address_number",
			"message": "La dirección debe estar entre 101 y 999 y no puede terminar en 00",
		})
		return
	}

	pool, err := GetPool()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "db_unavailable",
			"message": "No se pudo conectar a la base de datos",
		})
		return
	}

	repo := NewProfileRepository(pool)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	profile, err := repo.Create(ctx, user.ID, req.Username, req.AddressNumber)
	if err != nil {
		switch err {
		case ErrUsernameTaken:
			c.JSON(http.StatusConflict, gin.H{
				"success": false,
				"error":   "username_taken",
				"message": "El username ya está en uso",
			})
			return
		case ErrProfileAlreadyExists:
			c.JSON(http.StatusConflict, gin.H{
				"success": false,
				"error":   "profile_already_exists",
				"message": "El perfil ya existe",
			})
			return
		case ErrAddressTaken:
			c.JSON(http.StatusConflict, gin.H{
				"success": false,
				"error":   "address_taken",
				"message": "Esa dirección ya está ocupada",
			})
			return
		default:
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"error":   "profile_create_failed",
				"message": "No se pudo crear el perfil",
			})
			return
		}
	}

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"data":    profile,
	})
}
func GetUnreadCounts(c *gin.Context) {
	rawUser, exists := c.Get(ContextUserKey)
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"error":   "unauthorized",
			"message": "Usuario no autenticado",
		})
		return
	}

	user, ok := rawUser.(AuthUser)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "invalid_auth_context",
			"message": "Contexto de autenticación inválido",
		})
		return
	}

	pool, err := GetPool()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "db_unavailable",
			"message": "No se pudo conectar a la base de datos",
		})
		return
	}

	repo := NewMessageRepository(pool)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	items, err := repo.GetUnreadCounts(ctx, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "unread_counts_failed",
			"message": "No se pudieron obtener los no leídos",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    items,
	})
}
func ListUsers(c *gin.Context) {
	rawUser, exists := c.Get(ContextUserKey)
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"error":   "unauthorized",
			"message": "Usuario no autenticado",
		})
		return
	}

	user, ok := rawUser.(AuthUser)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "invalid_auth_context",
			"message": "Contexto de autenticación inválido",
		})
		return
	}

	limit := 999
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 999 {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "invalid_limit",
				"message": "limit debe estar entre 1 y 999",
			})
			return
		}
		limit = parsed
	}

	offset := 0
	if raw := c.Query("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "invalid_offset",
				"message": "offset inválido",
			})
			return
		}
		offset = parsed
	}

	pool, err := GetPool()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "db_unavailable",
			"message": "No se pudo conectar a la base de datos",
		})
		return
	}

	repo := NewProfileRepository(pool)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	users, err := repo.ListUsers(ctx, user.ID, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "users_list_failed",
			"message": "No se pudo obtener la lista de usuarios",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    users,
	})
}

// router
func init() {
	gin.SetMode(gin.ReleaseMode)

	router = gin.New()
	router.Use(gin.Recovery())

	router.Use(cors.New(cors.Config{
		AllowOrigins: []string{
			"http://localhost:5000",
			"http://127.0.0.1:5000",
			"http://localhost:5500",
			"http://127.0.0.1:5500",
			"https://tuky-front.vercel.app",
			"https://flash-chat.live",
			"https://www.flash-chat.live",
		},
		AllowMethods: []string{
			"GET",
			"POST",
			"PUT",
			"PATCH",
			"DELETE",
			"OPTIONS",
		},
		AllowHeaders: []string{
			"Origin",
			"Content-Length",
			"Content-Type",
			"Authorization",
			"Accept",
			"X-Requested-With",
		},
		ExposeHeaders: []string{
			"Content-Length",
		},
		AllowCredentials:          false,
		MaxAge:                    12 * time.Hour,
		OptionsResponseStatusCode: 200,
	}))

	router.OPTIONS("/*path", func(c *gin.Context) {
		c.Status(200)
	})

	api := router.Group("/api")
	{
		api.GET("/health", Health)

		protected := api.Group("")
		protected.Use(RequireAuth())
		{
			protected.GET("/me", Me)
			protected.POST("/profile/setup", ProfileSetup)
			protected.GET("/users", ListUsers)

			protected.POST("/friend-requests", CreateFriendRequest)
			protected.GET("/friend-requests/received", ListReceivedFriendRequests)
			protected.POST("/friend-requests/:id/accept", AcceptFriendRequest)

			protected.GET("/friends", ListFriends)

			protected.POST("/chats/:friendId/messages", CreateMessage)
			protected.GET("/chats/:friendId/messages", ListMessages)
			protected.POST("/chats/:friendId/read", MarkChatAsRead)

			protected.GET("/unread-counts", GetUnreadCounts)
		}
	}
}

func Handler(w http.ResponseWriter, r *http.Request) {
	router.ServeHTTP(w, r)
}
