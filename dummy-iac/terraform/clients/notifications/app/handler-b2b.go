package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Clave secreta para firmar los JWT. En producción, esto DEBE venir de os.Getenv("JWT_SECRET")
var jwtSecret = []byte(getEnvOrDefault("JWT_SECRET", "super-secret-key-for-magic-links"))

// --- Estructuras para los nuevos endpoints JWT ---

type GenerateInviteRequest struct {
	InviterID   string `json:"inviter_id"`
	CompanyID   string `json:"company_id"`
	Role        string `json:"role"`
	TargetEmail string `json:"target_email"` // Opcional
}

type RedeemInviteRequest struct {
	NewUserID   string `json:"new_user_id"`
	InviteToken string `json:"invite_token"`
}

type InviteClaims struct {
	InviterID string `json:"inviter_id"`
	CompanyID string `json:"company_id"`
	Role      string `json:"role"`
	Exp       int64  `json:"exp"`
}

type RevokeAccessRequest struct {
	ManagerID     string `json:"manager_id"`
	SubordinateID string `json:"subordinate_id"`
}

func getEnvOrDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

// --- Paso 1: Generar el Magic Link (Sin estado) ---
func handleGenerateInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req GenerateInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	// Crear el payload del JWT
	claims := InviteClaims{
		InviterID: req.InviterID,
		CompanyID: req.CompanyID,
		Role:      req.Role,
		Exp:       time.Now().Add(72 * time.Hour).Unix(), // El link expira en 3 días
	}

	// Serializar y codificar Header y Payload
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claimsJSON, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(claimsJSON)

	// Firmar el token
	signatureInput := header + "." + payload
	h := hmac.New(sha256.New, jwtSecret)
	h.Write([]byte(signatureInput))
	signature := base64.RawURLEncoding.EncodeToString(h.Sum(nil))

	jwtToken := signatureInput + "." + signature

	// Devolver el link mágico apuntando a front.primecore.online
	inviteLink := fmt.Sprintf("https://%s/registro?invite_token=%s", getEnvOrDefault("DOMAIN", "front.primecore.online"), jwtToken)

	// --- INTEGRACIÓN notify_service: Enviar correo de invitación si TargetEmail está presente ---
	if req.TargetEmail != "" {
		sendInternalNotification(NotificationPayload{
			Type:      "email",
			Recipient: req.TargetEmail,
			Subject:   "Invitación a Primecore B2B",
			Body:      "Has sido invitado a unirte a una organización en Primecore. Haz clic en el siguiente enlace para registrarte y aceptar la invitación: " + inviteLink,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":     "Magic link generado exitosamente",
		"invite_link": inviteLink,
	})
}

// --- Paso 2: Canjear el Token tras el registro ---
func handleRedeemInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req RedeemInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	// 1. Validar el JWT
	parts := strings.Split(req.InviteToken, ".")
	if len(parts) != 3 {
		http.Error(w, "Token inválido", http.StatusBadRequest)
		return
	}

	h := hmac.New(sha256.New, jwtSecret)
	h.Write([]byte(parts[0] + "." + parts[1]))
	expectedSignature := base64.RawURLEncoding.EncodeToString(h.Sum(nil))

	if parts[2] != expectedSignature {
		http.Error(w, "Token modificado o inválido", http.StatusUnauthorized)
		return
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		http.Error(w, "Error decodificando token", http.StatusInternalServerError)
		return
	}

	var claims InviteClaims
	json.Unmarshal(claimsJSON, &claims)

	if time.Now().Unix() > claims.Exp {
		http.Error(w, "El token de invitación ha expirado", http.StatusUnauthorized)
		return
	}

	// 2. Consolidar relación en Keto (Cascada)
	ketoWrite := getEnvOrDefault("KETO_WRITE_URL", "http://keto:4467")

	ketoPayload := map[string]string{
		"namespace":  "User",
		"object":     req.NewUserID,
		"relation":   "manager",
		"subject_id": claims.InviterID,
	}
	ketoBody, _ := json.Marshal(ketoPayload)
	reqKeto, _ := http.NewRequest(http.MethodPut, ketoWrite+"/admin/relation-tuples", bytes.NewBuffer(ketoBody))
	reqKeto.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	respKeto, err := client.Do(reqKeto)
	if err != nil || respKeto.StatusCode >= 300 {
		http.Error(w, "Falló la consolidación de jerarquía en Keto", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "Jerarquía consolidada exitosamente",
		"success": true,
	})
}

// --- Auditoría en Cascada ---
func handleGetHierarchy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) > 3 {
			userID = parts[3]
		} else {
			http.Error(w, "user_id is required", http.StatusBadRequest)
			return
		}
	}

	ketoRead := getEnvOrDefault("KETO_READ_URL", "http://keto:4466")
	subordinates := getSubordinatesRecursive(ketoRead, userID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"hierarchy_root": userID,
		"subordinates":   subordinates,
	})
}

func getSubordinatesRecursive(ketoReadURL, managerID string) []string {
	url := fmt.Sprintf("%s/relation-tuples?namespace=User&relation=manager&subject_id=%s", ketoReadURL, managerID)
	resp, err := http.Get(url)
	if err != nil {
		return []string{}
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	tuples, ok := result["relation_tuples"].([]interface{})
	if !ok {
		return []string{}
	}

	var directSubordinates []string
	for _, t := range tuples {
		tupleMap := t.(map[string]interface{})
		subID := tupleMap["object"].(string)
		directSubordinates = append(directSubordinates, subID)

		nested := getSubordinatesRecursive(ketoReadURL, subID)
		directSubordinates = append(directSubordinates, nested...)
	}

	return directSubordinates
}

// --- Paso 3: Revocar acceso de subordinado (Eliminar en Keto) ---
func handleRevokeAccess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req RevokeAccessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	if req.ManagerID == "" || req.SubordinateID == "" {
		http.Error(w, "Faltan parametros requeridos", http.StatusBadRequest)
		return
	}

	ketoWrite := getEnvOrDefault("KETO_WRITE_URL", "http://keto:4467")

	url := fmt.Sprintf("%s/admin/relation-tuples?namespace=User&object=%s&relation=manager&subject_id=%s",
		ketoWrite, req.SubordinateID, req.ManagerID)

	reqKeto, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		http.Error(w, "Error construyendo peticion interna", http.StatusInternalServerError)
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	respKeto, err := client.Do(reqKeto)

	if err != nil || (respKeto.StatusCode != http.StatusNoContent && respKeto.StatusCode != http.StatusOK) {
		http.Error(w, "Fallo la revocacion de jerarquia en Keto", http.StatusInternalServerError)
		return
	}
	if respKeto != nil && respKeto.Body != nil {
		defer respKeto.Body.Close()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "Permisos revocados exitosamente. El usuario ya no pertenece a la jerarquia.",
		"success": true,
	})
}

// =========================================================================
// MÓDULO DE INTEGRACIÓN DE CORREOS (notify_service)
// =========================================================================

// NotificationPayload define la estructura requerida por notify_service sin usar plantillas
type NotificationPayload struct {
	Type      string `json:"type"`
	Recipient string `json:"recipient"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
}

// sendInternalNotification dispara la petición HTTP a la instancia de flujos
func sendInternalNotification(payload NotificationPayload) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Error serializando payload de correo: %v", err)
		return
	}

	// URL base del notify_service en la instancia de flujos
	notifyURL := getEnvOrDefault("NOTIFY_SERVICE_URL", "http://35.95.140.247:8081") + "/api/v1/notify"

	reqNotify, err := http.NewRequest("POST", notifyURL, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Error creando HTTP request hacia notify_service: %v", err)
		return
	}
	reqNotify.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(reqNotify)

	if err != nil || resp.StatusCode >= 400 {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		log.Printf("Advertencia: Falló envío hacia notify_service para %s. HTTP Status: %v", payload.Recipient, status)
	} else {
		log.Printf("Notificación encolada exitosamente en notify_service para %s", payload.Recipient)
	}

	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
}
