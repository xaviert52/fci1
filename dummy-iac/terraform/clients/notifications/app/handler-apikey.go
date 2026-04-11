package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

// Estructura para recibir la petición
type GenerateAPIKeyRequest struct {
	UserID string `json:"user_id"`
}

// Genera un string criptográficamente seguro
func generateSecureKey() string {
	bytes := make([]byte, 24) // 192 bits de entropía
	if _, err := rand.Read(bytes); err != nil {
		log.Fatal(err)
	}
	return "pk_live_" + hex.EncodeToString(bytes)
}

// Endpoint para generar la llave y registrarla en APISIX
func handleGenerateAPIKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req GenerateAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" {
		http.Error(w, "Invalid payload. user_id is required", http.StatusBadRequest)
		return
	}

	// 1. Generar la nueva llave (API Key)
	newApiKey := generateSecureKey()

	// 2. Registrar al usuario como "Consumer" en APISIX
	// En tu docker-compose, APISIX se llama "gateway" y su API Admin corre en el 9180
	apisixAdminURL := getEnvOrDefault("APISIX_ADMIN_URL", "http://gateway:9180")
	// La llave por defecto de APISIX Admin (cámbiala en producción mediante variables de entorno)
	apisixAdminToken := getEnvOrDefault("APISIX_ADMIN_TOKEN", "edd1c9f034335f136f87ad84b625c8f1")

	apisixPayload := map[string]interface{}{
		"username": req.UserID,
		"plugins": map[string]interface{}{
			"key-auth": map[string]string{
				"key": newApiKey,
			},
		},
	}

	jsonData, _ := json.Marshal(apisixPayload)

	// Llamada a la API de administración de APISIX
	reqApisix, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/apisix/admin/consumers", apisixAdminURL), bytes.NewBuffer(jsonData))
	if err != nil {
		http.Error(w, "Error construyendo request hacia APISIX", http.StatusInternalServerError)
		return
	}

	reqApisix.Header.Set("Content-Type", "application/json")
	reqApisix.Header.Set("X-API-KEY", apisixAdminToken)

	client := &http.Client{}
	resp, err := client.Do(reqApisix)

	if err != nil || resp.StatusCode >= 300 {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		log.Printf("Error registrando consumer en APISIX. Status: %d. Error: %v", status, err)
		http.Error(w, "Fallo al registrar la API Key en el Gateway", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// 3. Responderle al usuario con su llave lista para usar
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "API Key generada y registrada en el Gateway exitosamente",
		"user_id": req.UserID,
		"api_key": newApiKey,
	})
}
