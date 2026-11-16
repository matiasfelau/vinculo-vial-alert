package postgres

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"vinculo-vial-alert/internal/domain"
)

// AdminRepository implementa domain.AdminAlertRepository sobre
// GORM/PostgreSQL. Lee solo la copia de Alert.
type AdminRepository struct {
	db *gorm.DB
}

var _ domain.AdminAlertRepository = (*AdminRepository)(nil)

// NewAdminRepository construye un AdminRepository a partir de una conexión
// GORM ya establecida.
func NewAdminRepository(db *gorm.DB) *AdminRepository {
	return &AdminRepository{db: db}
}

// reportedAlertRow es la proyección del agregado por alerta. No incluye
// owner_id ni el motivo de los reportes: el operador sabe cuántos dueños
// reportaron, nunca quiénes.
type reportedAlertRow struct {
	ID              uuid.UUID `gorm:"column:id"`
	AlertType       string    `gorm:"column:alert_type"`
	CreatedAt       string    `gorm:"column:created_at"`
	ValidatedAt     *string   `gorm:"column:validated_at"`
	FirstReportedAt string    `gorm:"column:first_reported_at"`
	ReportCount     int       `gorm:"column:report_count"`
}

// ListReportedByIssuer implementa domain.AdminAlertRepository. Dos consultas,
// sin N+1: el agregado de reportes por alerta y las imágenes redactadas de
// todas ellas.
func (r *AdminRepository) ListReportedByIssuer(ctx context.Context, issuerID uuid.UUID) ([]domain.ReportedAlert, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var rows []reportedAlertRow
	err := r.db.WithContext(ctx).
		Table(alertsTable).
		Select("alerts.id AS id, alerts.alert_type AS alert_type, alerts.created_at AS created_at, alerts.validated_at AS validated_at, "+
			"MIN(alert_reports.created_at) AS first_reported_at, COUNT(alert_reports.owner_id) AS report_count").
		Joins("JOIN alert_reports ON alert_reports.alert_id = alerts.id").
		Where("alerts.issuer_id = ?", issuerID).
		Group("alerts.id, alerts.alert_type, alerts.created_at, alerts.validated_at").
		Order("first_reported_at ASC, alerts.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, classifyDBError(err)
	}

	evidence, err := r.redactedEvidenceOf(ctx, rows)
	if err != nil {
		return nil, err
	}

	alerts := make([]domain.ReportedAlert, len(rows))
	for i, row := range rows {
		alert, err := reportedAlertFromRow(row)
		if err != nil {
			return nil, err
		}
		alert.Evidence = evidence[row.ID]
		alerts[i] = alert
	}
	return alerts, nil
}

func reportedAlertFromRow(row reportedAlertRow) (domain.ReportedAlert, error) {
	createdAt, err := parseCreatedAt(row.CreatedAt)
	if err != nil {
		return domain.ReportedAlert{}, fmt.Errorf("admin repository: created_at con formato inesperado: %w", err)
	}
	firstReportedAt, err := parseCreatedAt(row.FirstReportedAt)
	if err != nil {
		return domain.ReportedAlert{}, fmt.Errorf("admin repository: first_reported_at con formato inesperado: %w", err)
	}

	alert := domain.ReportedAlert{
		ID:              row.ID,
		AlertType:       domain.AlertType(row.AlertType),
		CreatedAt:       createdAt.UTC(),
		FirstReportedAt: firstReportedAt.UTC(),
		ReportCount:     row.ReportCount,
	}
	if row.ValidatedAt != nil {
		validatedAt, err := parseCreatedAt(*row.ValidatedAt)
		if err != nil {
			return domain.ReportedAlert{}, fmt.Errorf("admin repository: validated_at con formato inesperado: %w", err)
		}
		validatedAt = validatedAt.UTC()
		alert.ValidatedAt = &validatedAt
	}
	return alert, nil
}

// redactedEvidenceOf lee, con una única consulta, las imágenes redactadas de
// las alertas de la lista, ordenadas por rol e índice como las del buzón.
func (r *AdminRepository) redactedEvidenceOf(ctx context.Context, rows []reportedAlertRow) (map[uuid.UUID][]domain.ReportedEvidence, error) {
	evidence := make(map[uuid.UUID][]domain.ReportedEvidence, len(rows))
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
		evidence[row.AlertID] = append(evidence[row.AlertID], domain.ReportedEvidence{Role: domain.EvidenceRole(row.Role), Index: row.Idx})
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

// FindReportedEvidence implementa domain.AdminAlertRepository. La condición de
// que la alerta tenga al menos un reporte mantiene el puerto de administración
// acotado a su finalidad: revisar lo reportado, no navegar toda la evidencia.
func (r *AdminRepository) FindReportedEvidence(ctx context.Context, alertID uuid.UUID, role domain.EvidenceRole, index int) (domain.EvidenceAccess, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var rows []evidenceAccessRow
	err := r.db.WithContext(ctx).
		Table(alertsTable).
		Joins("JOIN alert_evidence ON alert_evidence.alert_id = alerts.id").
		Where("alerts.id = ? AND alert_evidence.kind = ? AND alert_evidence.role = ? AND alert_evidence.idx = ?",
			alertID, string(domain.EvidenceKindRedacted), string(role), index).
		Where("EXISTS (SELECT 1 FROM alert_reports WHERE alert_reports.alert_id = alerts.id)").
		Select("alert_evidence.s3_key AS s3_key, alerts.validated_at AS validated_at").
		Scan(&rows).Error
	if err != nil {
		return domain.EvidenceAccess{}, classifyDBError(err)
	}
	if len(rows) == 0 {
		return domain.EvidenceAccess{}, domain.ErrReportedEvidenceNotFound
	}

	access := domain.EvidenceAccess{Key: rows[0].S3Key}
	if rows[0].ValidatedAt != nil {
		validatedAt, err := parseCreatedAt(*rows[0].ValidatedAt)
		if err != nil {
			return domain.EvidenceAccess{}, fmt.Errorf("admin repository: validated_at con formato inesperado: %w", err)
		}
		validatedAt = validatedAt.UTC()
		access.ValidatedAt = &validatedAt
	}
	return access, nil
}
