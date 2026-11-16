package postgres

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

// inboxRow es la proyección exacta del JOIN entre alerts, alert_destinations
// y las valoraciones, reportes y lecturas del propio destinatario. No incluye
// issuer_id ni owner_id: solo lo que el DTO de salida puede exponer.
//
// created_at y validated_at se escanean como string: el driver de PostgreSQL
// entrega un timestamptz, que database/sql formatea como RFC3339Nano al
// asignarlo a un *string; el de SQLite (usado en los tests) no reconoce
// "timestamptz" como columna de fecha y devuelve el texto tal cual lo
// escribió. createdAtLayouts cubre ambos casos.
type inboxRow struct {
	ID             uuid.UUID `gorm:"column:id"`
	AlertType      string    `gorm:"column:alert_type"`
	CreatedAt      string    `gorm:"column:created_at"`
	RequiresReview bool      `gorm:"column:requires_review"`
	ValidatedAt    *string   `gorm:"column:validated_at"`
	MyFeedback     *string   `gorm:"column:my_feedback"`
	Reported       bool      `gorm:"column:reported"`
	Seen           bool      `gorm:"column:seen"`
}

// inboxEvidenceRow es una imagen redactada de una alerta de la página.
type inboxEvidenceRow struct {
	AlertID uuid.UUID `gorm:"column:alert_id"`
	Role    string    `gorm:"column:role"`
	Idx     int       `gorm:"column:idx"`
}

var createdAtLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
}

func parseCreatedAt(s string) (time.Time, error) {
	var lastErr error
	for _, layout := range createdAtLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		} else {
			lastErr = err
		}
	}
	return time.Time{}, lastErr
}

// evidenceRoleOrder fija el orden en que el buzón lista las imágenes de una
// alerta: primero el vehículo y luego la patente.
var evidenceRoleOrder = map[domain.EvidenceRole]int{
	domain.EvidenceRoleGeneral: 0,
	domain.EvidenceRolePlate:   1,
}

// InboxRepository implementa domain.InboxRepository sobre GORM/PostgreSQL.
type InboxRepository struct {
	db *gorm.DB
}

// NewInboxRepository construye un InboxRepository a partir de una conexión
// GORM ya establecida.
func NewInboxRepository(db *gorm.DB) *InboxRepository {
	return &InboxRepository{db: db}
}

// FindByOwner ejecuta el JOIN alerts/alert_destinations filtrado por
// owner_id y por alertas ACTIVE (una alerta pendiente, expirada o rechazada
// no es visible), con proyección explícita de columnas (prohibido SELECT *),
// cláusula de tupla del cursor si viene, y LIMIT limit+1 para que el caso
// de uso pueda detectar si hay próxima página sin un COUNT adicional.
//
// La valoración y el reporte del propio destinatario salen de LEFT JOIN
// acotados a su owner_id, y las imágenes redactadas de toda la página se leen
// con una única consulta adicional: sin N+1.
func (r *InboxRepository) FindByOwner(ctx context.Context, ownerID uuid.UUID, cursor *domain.Cursor, limit int) ([]domain.InboxItem, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	query := r.db.WithContext(ctx).
		Table("alerts").
		Select("alerts.id, alerts.alert_type, alerts.created_at, alerts.requires_review, alerts.validated_at, "+
			"alert_feedback.rating AS my_feedback, alert_reports.owner_id IS NOT NULL AS reported, "+
			"alert_destinations.seen_at IS NOT NULL AS seen").
		Joins("JOIN alert_destinations ON alert_destinations.alert_id = alerts.id").
		Joins("LEFT JOIN alert_feedback ON alert_feedback.alert_id = alerts.id AND alert_feedback.owner_id = ?", ownerID).
		Joins("LEFT JOIN alert_reports ON alert_reports.alert_id = alerts.id AND alert_reports.owner_id = ?", ownerID).
		Where("alert_destinations.owner_id = ? AND alerts.status = ?", ownerID, string(domain.AlertStatusActive))

	if cursor != nil {
		// Comparación por tupla (created_at, id) < (cursor.CreatedAt, cursor.ID),
		// expandida como OR para ser portable entre PostgreSQL y el SQLite de
		// los tests.
		query = query.Where(
			"alerts.created_at < ? OR (alerts.created_at = ? AND alerts.id < ?)",
			cursor.CreatedAt, cursor.CreatedAt, cursor.ID,
		)
	}

	var rows []inboxRow
	if err := query.
		Order("alerts.created_at DESC, alerts.id DESC").
		Limit(limit + 1).
		Scan(&rows).Error; err != nil {
		return nil, classifyDBError(err)
	}

	evidence, err := r.redactedEvidenceByAlert(ctx, rows)
	if err != nil {
		return nil, err
	}

	items := make([]domain.InboxItem, len(rows))
	for i, row := range rows {
		item, err := inboxItemFromRow(row)
		if err != nil {
			return nil, err
		}
		item.Evidence = evidence[row.ID]
		items[i] = item
	}
	return items, nil
}

func inboxItemFromRow(row inboxRow) (domain.InboxItem, error) {
	createdAt, err := parseCreatedAt(row.CreatedAt)
	if err != nil {
		return domain.InboxItem{}, fmt.Errorf("inbox repository: created_at con formato inesperado: %w", err)
	}

	item := domain.InboxItem{
		ID:             row.ID,
		AlertType:      domain.AlertType(row.AlertType),
		CreatedAt:      createdAt,
		RequiresReview: row.RequiresReview,
		Reported:       row.Reported,
		Seen:           row.Seen,
	}
	if row.ValidatedAt != nil {
		validatedAt, err := parseCreatedAt(*row.ValidatedAt)
		if err != nil {
			return domain.InboxItem{}, fmt.Errorf("inbox repository: validated_at con formato inesperado: %w", err)
		}
		validatedAt = validatedAt.UTC()
		item.ValidatedAt = &validatedAt
	}
	if row.MyFeedback != nil {
		rating := domain.Rating(*row.MyFeedback)
		item.MyFeedback = &rating
	}
	return item, nil
}

// redactedEvidenceByAlert lee, con una única consulta, las imágenes redactadas
// de las alertas de la página, ordenadas por rol e índice.
func (r *InboxRepository) redactedEvidenceByAlert(ctx context.Context, rows []inboxRow) (map[uuid.UUID][]domain.InboxEvidence, error) {
	evidence := make(map[uuid.UUID][]domain.InboxEvidence, len(rows))
	if len(rows) == 0 {
		return evidence, nil
	}

	alertIDs := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		alertIDs[i] = row.ID
	}

	var found []inboxEvidenceRow
	err := r.db.WithContext(ctx).
		Table("alert_evidence").
		Select("alert_id, role, idx").
		Where("kind = ? AND alert_id IN ?", string(domain.EvidenceKindRedacted), alertIDs).
		Scan(&found).Error
	if err != nil {
		return nil, classifyDBError(err)
	}

	for _, row := range found {
		evidence[row.AlertID] = append(evidence[row.AlertID], domain.InboxEvidence{Role: domain.EvidenceRole(row.Role), Index: row.Idx})
	}
	for _, list := range evidence {
		sort.Slice(list, func(i, j int) bool {
			if list[i].Role != list[j].Role {
				return evidenceRoleOrder[list[i].Role] < evidenceRoleOrder[list[j].Role]
			}
			return list[i].Index < list[j].Index
		})
	}
	return evidence, nil
}
