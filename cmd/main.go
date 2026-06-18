package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/Endea4/studExE4-shared/config"
	"github.com/google/uuid"
	"github.com/Endea4/studExE4-shared/pricing"
	"github.com/Endea4/studExE4-shared/auth"

	_ "github.com/Endea4/studExE4-api-gateway/docs"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

func logErr(format string, args ...interface{}) {
	log.Printf("[ERROR] "+format, args...)
}

func newProxy(target string) gin.HandlerFunc {
	u, err := url.Parse(target)
	if err != nil {
		logErr("proxy URL parse failed: target=%s err=%v", target, err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	return func(c *gin.Context) {
		c.Request.URL.Host = u.Host
		c.Request.URL.Scheme = u.Scheme
		c.Request.Header.Set("X-Forwarded-Host", c.Request.Host)
		c.Request.Host = u.Host
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

func callService(method, target string, body map[string]interface{}) (map[string]interface{}, error) {
	var bodyBytes []byte
	if body != nil {
		bodyBytes, _ = json.Marshal(body)
	}

	req, err := http.NewRequest(method, target, bytes.NewReader(bodyBytes))
	if err != nil {
		logErr("callService new request failed: %s %s err=%v", method, target, err)
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		logErr("callService request failed: %s %s err=%v", method, target, err)
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(respBody, &result)

	if resp.StatusCode >= 400 {
		logErr("callService upstream error: %s %s status=%d", method, target, resp.StatusCode)
		return nil, fmt.Errorf("error: %d", resp.StatusCode)
	}
	return result, nil
}

func callServiceRaw(method, target string, body map[string]interface{}) (interface{}, error) {
	var bodyBytes []byte
	if body != nil {
		bodyBytes, _ = json.Marshal(body)
	}
	req, err := http.NewRequest(method, target, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("error: %d", resp.StatusCode)
	}
	var result interface{}
	json.Unmarshal(respBody, &result)
	return result, nil
}

var (
	userProxy     gin.HandlerFunc
	locationProxy gin.HandlerFunc
	tripProxy     gin.HandlerFunc
	matchingProxy gin.HandlerFunc
	ratingProxy   gin.HandlerFunc
)

// @title StudEx API Gateway
// @version 1.0
// @description BFF (Backend for Frontend) that proxies to downstream services.
// @host localhost:9080
// @BasePath /
// @schemes http https
func main() {
	config.LoadConfig()
	userSvc := config.GetEnv("USER_SERVICE_URL", "http://localhost:9086")
	locationSvc := config.GetEnv("LOCATION_SERVICE_URL", "http://localhost:9083")
	tripSvc := config.GetEnv("TRIP_SERVICE_URL", "http://localhost:9085")
	matchingSvc := config.GetEnv("MATCHING_SERVICE_URL", "http://localhost:9084")
	ratingSvc := config.GetEnv("RATING_SERVICE_URL", "http://localhost:9087")
	userProxy = newProxy(userSvc)
	locationProxy = newProxy(locationSvc)
	tripProxy = newProxy(tripSvc)
	matchingProxy = newProxy(matchingSvc)
	ratingProxy = newProxy(ratingSvc)

	r := gin.Default()

	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"service": "api-gateway", "status": "running"})
	})

	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	r.GET("/info", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"service": "api-gateway",
			"upstream_services": []gin.H{
				{"name": "user-service", "url": userSvc},
				{"name": "location-service", "url": locationSvc},
				{"name": "trip-service", "url": tripSvc},
				{"name": "matching-service", "url": matchingSvc},
				{"name": "rating-service", "url": ratingSvc},
			},
			"routes": []string{
				"ANY /users/*path",
				"ANY /auth/*path",
				"ANY /drivers/*path",
				"ANY /location/*path",
				"ANY /trips/*path",
				"ANY /match/*path",
				"ANY /ratings/*path",
				"ANY /pending-ratings/*path",
				"ANY /reputation/*path",
				"POST /rides/request",
				"POST /rides/:trip_id/rematch",
				"GET /rides/price",
			},
		})
	})
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	r.Any("/location-swagger/*path", func(c *gin.Context) {
		c.Request.URL.Path = "/swagger" + c.Request.URL.Path[len("/location-swager"):]
		locationProxy(c)
	})

	r.Any("/users/*path", userProxy)
	r.Any("/users", userProxy)
	r.Any("/auth/*path", userProxy)
	r.Any("/auth", userProxy)

	registerDriverBFF(r, userSvc, ratingSvc)

	r.Any("/location/*path", locationProxy)
	r.Any("/location", locationProxy)

	r.Any("/trips/*path", tripProxy)
	r.Any("/trips", tripProxy)

	r.Any("/match/*path", matchingProxy)
	r.Any("/match", matchingProxy)

	r.Any("/ratings/*path", ratingProxy)
	r.Any("/ratings", ratingProxy)
	r.Any("/pending-ratings/*path", ratingProxy)
	r.Any("/pending-ratings", ratingProxy)
	r.Any("/reputation/*path", ratingProxy)
	r.Any("/reputation", ratingProxy)

	// Bot admin proxy
	botURL := config.GetEnv("WHATSAPP_BOT_URL", "http://localhost:9088")
	botProxy := newProxy(botURL)
	r.Any("/admin/*path", botProxy)
	r.Any("/admin", botProxy)

	r.POST("/rides/request", func(c *gin.Context) {
		ridesRequestBFF(c, matchingSvc, userSvc, tripSvc)
	})

	r.POST("/rides/:trip_id/rematch", func(c *gin.Context) {
		rematchBFF(c, matchingSvc, tripSvc)
	})

	r.GET("/rides/price", func(c *gin.Context) {
		priceEstimateBFF(c)
	})

	r.NoRoute(func(c *gin.Context) {
		logErr("NoRoute: path=%s method=%s", c.Request.URL.Path, c.Request.Method)
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	})

	addr := config.GetEnv("GATEWAY_PORT", ":9080")
	fmt.Printf("API Gateway running on %s\n", addr)
	r.Run(addr)
}

// ─── BFF: Ride Request ───
type rideRequestBody struct {
	CustomerRefID string  `json:"customer_ref_id" binding:"required"`
	PickupLat     float64 `json:"pickup_lat" binding:"required"`
	PickupLng     float64 `json:"pickup_lng" binding:"required"`
	DestLat       float64 `json:"dest_lat" binding:"required"`
	DestLng       float64 `json:"dest_lng" binding:"required"`
	ServiceType   string  `json:"service_type"`
	GenderPref    string  `json:"gender_pref"`
}

func ridesRequestBFF(c *gin.Context, matchingSvc, userSvc, tripSvc string) {
	var req rideRequestBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	cfg := pricing.GetConfig(req.ServiceType)
	distance := pricing.CalculateDistance(req.PickupLat, req.PickupLng, req.DestLat, req.DestLng)
	breakdown := pricing.CalculatePrice(cfg, distance)

	attributes := map[string]interface{}{}
	if req.GenderPref != "" {
		attributes["gender"] = strings.ToUpper(req.GenderPref)
	}
	attributes["dest_lat"] = req.DestLat
	attributes["dest_lng"] = req.DestLng
	attributes["service_type"] = req.ServiceType
	attributes["estimated_price"] = breakdown.Total

	orderID := uuid.New().String()
	matchPayload := map[string]interface{}{
		"order_id":        orderID,
		"customer_ref_id": req.CustomerRefID,
		"pickup_lat":      req.PickupLat,
		"pickup_lng":      req.PickupLng,
		"attributes":      attributes,
	}

	matchResp, err := callService("POST", matchingSvc+"/match/requests", matchPayload)
	if err != nil {
		logErr("ridesRequestBFF: match-svc call failed: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to submit match request", "detail": err.Error()})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"order_id":    orderID,
		"request_id":  matchResp["request_id"],
		"status":      matchResp["status"],
		"price":       breakdown,
		"distance_km": breakdown.DistanceKm,
	})
}

// ─── BFF: Price Estimate ───
func priceEstimateBFF(c *gin.Context) {
	var query struct {
		OriginLat   float64 `form:"origin_lat" binding:"required"`
		OriginLng   float64 `form:"origin_lng" binding:"required"`
		DestLat     float64 `form:"dest_lat" binding:"required"`
		DestLng     float64 `form:"dest_lng" binding:"required"`
		ServiceType string  `form:"service_type"`
	}
	if err := c.ShouldBindQuery(&query); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	cfg := pricing.GetConfig(query.ServiceType)
	distance := pricing.CalculateDistance(query.OriginLat, query.OriginLng, query.DestLat, query.DestLng)
	breakdown := pricing.CalculatePrice(cfg, distance)

	c.JSON(http.StatusOK, breakdown)
}

// ─── BFF: Rematch on reject ───
func rematchBFF(c *gin.Context, matchingSvc, tripSvc string) {
	tripID := c.Param("trip_id")

	tripResp, err := callService("GET", tripSvc+"/trips/"+tripID, nil)
	if err != nil {
		logErr("rematchBFF: trip-svc call failed: trip_id=%s err=%v", tripID, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "trip not found"})
		return
	}

	orderID, _ := tripResp["order_id"].(string)
	customerRefID, _ := tripResp["customer_ref_id"].(string)
	pickupLat, _ := tripResp["pickup_lat"].(float64)
	pickupLng, _ := tripResp["pickup_lng"].(float64)

	if orderID == "" || customerRefID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "trip missing required fields for rematch"})
		return
	}

	attrs, _ := tripResp["attributes"].(map[string]interface{})
	if attrs == nil {
		attrs = map[string]interface{}{}
	}
	if rejectedDriver, ok := tripResp["driver_ref_id"].(string); ok && rejectedDriver != "" {
		if prev, ok := attrs["excluded_drivers"].([]interface{}); ok {
			attrs["excluded_drivers"] = append(prev, rejectedDriver)
		} else {
			attrs["excluded_drivers"] = []interface{}{rejectedDriver}
		}
	}

	matchPayload := map[string]interface{}{
		"order_id":        orderID,
		"customer_ref_id": customerRefID,
		"pickup_lat":      pickupLat,
		"pickup_lng":      pickupLng,
		"attributes":      attrs,
	}

	matchResp, err := callService("POST", matchingSvc+"/match/requests", matchPayload)
	if err != nil {
		logErr("rematchBFF: match-svc call failed: trip_id=%s err=%v", tripID, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to resubmit match request"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"trip_id":     tripID,
		"request_id":  matchResp["request_id"],
		"status":      "searching",
	})
}

// ─── BFF: Driver routes (merged from driver-service into user-service) ───

func phoneFromRequest(c *gin.Context) string {
	if phone := c.Query("phone"); phone != "" {
		return phone
	}
	authHeader := c.GetHeader("Authorization")
	if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
		claims, err := auth.ValidateJWT(authHeader[7:])
		if err == nil && claims.Phone != "" {
			return claims.Phone
		}
	}
	return ""
}

func resolveUserID(userSvc, phone string) string {
	if phone == "" {
		return ""
	}
	resp, err := callService("GET", userSvc+"/users/"+phone, nil)
	if err != nil {
		return phone
	}
	if id, ok := resp["id"].(string); ok && id != "" {
		return id
	}
	return phone
}

func registerDriverBFF(r *gin.Engine, userSvc, ratingSvc string) {
	r.POST("/drivers/auth", func(c *gin.Context) {
		var req struct {
			Phone    string `json:"phone" binding:"required"`
			Password string `json:"password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		resp, err := callService("POST", userSvc+"/auth/login", map[string]interface{}{
			"phone":    req.Phone,
			"password": req.Password,
		})
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
			return
		}
		if user, ok := resp["user"].(map[string]interface{}); ok {
			if roles, ok := user["roles"].([]interface{}); ok {
				isDriver := false
				for _, r := range roles {
					if r == "DRIVER" {
						isDriver = true
						break
					}
				}
				if !isDriver {
					c.JSON(http.StatusUnauthorized, gin.H{"error": "not a driver account"})
					return
				}
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"driver": resp["user"],
			"token":  resp["token"],
		})
	})

	r.POST("/drivers/add-role", func(c *gin.Context) {
		var req struct {
			Phone       string `json:"phone" binding:"required"`
			VehicleType string `json:"vehicle_type"`
			PlateNumber string `json:"plate_number"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		user, err := callService("GET", userSvc+"/users/"+req.Phone, nil)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}

		if roles, ok := user["roles"].([]interface{}); ok {
			for _, r := range roles {
				if r == "DRIVER" {
					c.JSON(http.StatusConflict, gin.H{"error": "user is already a driver"})
					return
				}
			}
		}

		body := map[string]interface{}{"phone": req.Phone}
		if req.VehicleType != "" {
			body["vehicle_type"] = req.VehicleType
		}
		if req.PlateNumber != "" {
			body["plate_number"] = req.PlateNumber
		}
		_, err = callService("POST", userSvc+"/drivers/add-role", body)
		if err != nil {
			logErr("add-role upstream failed: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add driver role"})
			return
		}

		hashedPassword, _ := auth.HashPassword(req.Phone)
		callService("PUT", userSvc+"/users/"+req.Phone, map[string]interface{}{"password": hashedPassword})

		updated, _ := callService("GET", userSvc+"/users/"+req.Phone, nil)
		id, _ := updated["id"].(string)
		phone := ""
		if phones, ok := updated["phone"].([]interface{}); ok && len(phones) > 0 {
			phone, _ = phones[0].(string)
		}
		roles := []string{}
		if r, ok := updated["roles"].([]interface{}); ok {
			for _, v := range r {
				if s, ok := v.(string); ok {
					roles = append(roles, s)
				}
			}
		}
		token, _ := auth.GenerateJWT(id, phone, fmt.Sprintf("%v", roles))
		c.JSON(http.StatusOK, gin.H{
			"user":  updated,
			"token": token,
		})
	})

	r.GET("/drivers", func(c *gin.Context) {
		phone := c.Query("phone")
		if phone != "" {
			userProxy(c)
			return
		}
		userID := c.Query("user_id")
		if userID != "" {
			c.Request.URL.Path = "/users/" + userID
			userProxy(c)
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "phone or user_id query param required"})
	})

	r.GET("/drivers/active", func(c *gin.Context) {
		userProxy(c)
	})

	r.GET("/drivers/all", func(c *gin.Context) {
		userProxy(c)
	})

	r.PUT("/drivers/status", func(c *gin.Context) {
		userProxy(c)
	})

	r.PUT("/drivers/online", func(c *gin.Context) {
		userProxy(c)
	})

	r.Any("/drivers/debts/*path", func(c *gin.Context) {
		phone := phoneFromRequest(c)
		if phone != "" && c.Query("phone") == "" {
			c.Request.URL.RawQuery = "phone=" + phone
		}
		userProxy(c)
	})
	r.GET("/drivers/debts", func(c *gin.Context) {
		phone := phoneFromRequest(c)
		if phone != "" && c.Query("phone") == "" {
			c.Request.URL.RawQuery = "phone=" + phone
		}
		userProxy(c)
	})

	r.Any("/drivers/leave-requests/*path", func(c *gin.Context) {
		userProxy(c)
	})
	r.Any("/drivers/leave-requests", func(c *gin.Context) {
		userProxy(c)
	})

	r.GET("/drivers/orders", func(c *gin.Context) {
		phone := phoneFromRequest(c)
		if phone == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "phone query param or Authorization token required"})
			return
		}
		userID := resolveUserID(userSvc, phone)
		c.Request.URL.Path = "/trips/driver/" + userID
		tripProxy(c)
	})

	r.GET("/drivers/reputation", func(c *gin.Context) {
		phone := phoneFromRequest(c)
		if phone == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "phone query param or Authorization token required"})
			return
		}
		userID := resolveUserID(userSvc, phone)

		rep, _ := callService("GET", ratingSvc+"/reputation/"+userID+"?role=driver", nil)

		ratingsResp, _ := callServiceRaw("GET", ratingSvc+"/ratings/"+userID+"?rater_type=customer", nil)

		var reviews []map[string]interface{}
		if ratingsList, ok := ratingsResp.([]interface{}); ok {
			nameCache := map[string]string{}
			for _, item := range ratingsList {
				r, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				if status, _ := r["status"].(string); status == "rejected" || status == "pending" {
					continue
				}
				raterID, _ := r["rater_id"].(string)
				raterName := "Anonim"
				if raterID != "" {
					if cached, ok := nameCache[raterID]; ok {
						raterName = cached
					} else {
						if userResp, err := callService("GET", userSvc+"/users/"+raterID, nil); err == nil {
							if name, ok := userResp["name"].(string); ok && name != "" {
								raterName = name
							}
						}
						nameCache[raterID] = raterName
					}
				}
				score := 0
				if s, ok := r["score"].(float64); ok {
					score = int(s)
				}
				reviewText, _ := r["review"].(string)
				reviews = append(reviews, map[string]interface{}{
					"rater_name": raterName,
					"score":      score,
					"review":     reviewText,
					"created_at": r["created_at"],
				})
			}
		}
		if reviews == nil {
			reviews = []map[string]interface{}{}
		}

		result := map[string]interface{}{
			"score":         0.0,
			"total_reviews": 0,
			"last_updated":  nil,
			"reviews":       reviews,
		}
		if rep != nil {
			if s, ok := rep["score"].(float64); ok {
				result["score"] = s
			}
			if tr, ok := rep["total_reviews"]; ok {
				result["total_reviews"] = tr
			}
			if lu, ok := rep["last_updated"]; ok {
				result["last_updated"] = lu
			}
		}
		c.JSON(http.StatusOK, result)
	})

	r.GET("/drivers/ratings/pending", func(c *gin.Context) {
		phone := phoneFromRequest(c)
		if phone == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "phone query param or Authorization token required"})
			return
		}
		userID := resolveUserID(userSvc, phone)
		c.Request.URL.Path = "/pending-ratings/" + userID
		c.Request.URL.RawQuery = "role=driver"
		ratingProxy(c)
	})

	r.POST("/drivers/ratings/:id", func(c *gin.Context) {
		ratingID := c.Param("id")

		var body struct {
			Score  int    `json:"score" binding:"required"`
			Review string `json:"review"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		phone := phoneFromRequest(c)
		if phone == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization required"})
			return
		}
		driverID := resolveUserID(userSvc, phone)

		pendingResp, err := http.Get(ratingSvc + "/pending-ratings/" + driverID + "?role=driver")
		if err != nil || pendingResp.StatusCode != 200 {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to lookup pending rating"})
			return
		}
		pendingBody, _ := io.ReadAll(pendingResp.Body)
		pendingResp.Body.Close()

		var pendingList []map[string]interface{}
		json.Unmarshal(pendingBody, &pendingList)

		var pending map[string]interface{}
		for _, p := range pendingList {
			if pid, _ := p["id"].(string); pid == ratingID {
				pending = p
				break
			}
		}
		if pending == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "pending rating not found"})
			return
		}

		customerID, _ := pending["customer_id"].(string)
		orderID, _ := pending["order_id"].(string)
		tripID, _ := pending["trip_id"].(string)

		submitPayload := map[string]interface{}{
			"order_id":   orderID,
			"trip_id":    tripID,
			"rater_type": "driver",
			"rater_id":   driverID,
			"ratee_id":   customerID,
			"score":      body.Score,
			"review":     body.Review,
		}

		result, err := callService("POST", ratingSvc+"/ratings/", submitPayload)
		if err != nil {
			logErr("drivers/ratings/:id submit failed: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to submit rating"})
			return
		}
		c.JSON(http.StatusCreated, result)
	})
	r.GET("/drivers/ratings", func(c *gin.Context) {
		ratingProxy(c)
	})
}
