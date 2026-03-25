---
name: agent-memory
description: Управление долгосрочной памятью проекта через MCP (Model Context Protocol) для сохранения контекста между сессиями.
---

## Состояние Памяти (Memory State)

У тебя есть доступ к persistent memory через MCP-инструменты. Твоя задача — поддерживать актуальность "Банка Проекта" (Project Bank), минимизируя шум.

### Когда сохранять (store_memory):
* **Принято архитектурное решение**: тип `semantic`, важность `0.9`.
* **Выработан рабочий процесс**: деплой, тесты, специфичный debug — тип `procedural`, важность `0.8`.
* **Найден баг или ограничение**: описание решения и причин — тип `episodic`, важность `0.7`.
* **Текущий контекст задачи**: над чем работаем в данный момент — тип `working`, важность `0.5`.

### Специализированные типы:
* **Архитектурное решение**: используй `store_decision` с rationale и consequences.
* **Инцидент**: используй `store_incident` с impact, root cause, severity.
* **Runbook**: используй `store_runbook` с procedure, trigger, verification, rollback.
* **Postmortem**: используй `store_postmortem` с root cause и action items.

### Когда вспоминать (recall_memory):
* **Начало новой сессии**: вызови `summarize_project_context` или `project_bank_view view=canonical_overview` для восстановления рабочего контекста.
* **Перед изменением инфраструктуры**: проверь `search_runbooks` и `recall_similar_incidents` на наличие рисков.
* **При вопросе "как делать X"**: поиск по типу `procedural`.
* **При вопросе "почему так решили"**: используй `recall_canonical_knowledge` или `project_bank_view view=decisions`.

---

## Жизненный цикл сессии (Session Lifecycle)

### 1. Start-of-session Recall
Перед началом работы ты обязан восстановить контекст:
* Используй `project_bank_view view=canonical_overview` для верхнеуровневого понимания.
* Изучи недавние изменения в сервисах или компонентах, которые предстоит затронуть.
* Найди релевантные RFC, заметки о инцидентах или чейнджлоги через `semantic_search`.
* Проверь `steward_status` — есть ли pending review items или недавние findings.

### 2. In-session Patterns
* Решения фиксируй сразу через `store_decision` — не откладывай на конец сессии.
* Длинные трейсы и логи — `store_memory` (episodic), в чат только 5-8 строк.
* Перед исследованием файлов — сначала `recall_memory` по теме, чтобы не искать заново.

### 3. Close-session Patterns
В конце работы вызови `close_session` с кратким резюме:
* **Coding close**: Опиши изменения. Если сессия была исследовательской и шумной, используй `save_raw_only`.
* **Incident close**: Режим `mode=incident`. Обязательно укажи влияние (impact), способы отката (rollback) и нерешенные вопросы.
* **Migration close**: Режим `mode=migration`. Укажи последовательность шагов, зависимости и проверку после деплоя.

---

## Knowledge Stewardship

### Обслуживание памяти
Сервис поддерживает автоматическое и ручное обслуживание knowledge base.

* **Запуск полного цикла**: `steward_run scope=full` — поиск дублей, конфликтов, устаревших записей, кандидатов в canonical.
* **Только отчёт**: `steward_run scope=full dry_run=true` — ничего не меняет, только показывает findings.
* **Просмотр результатов**: `steward_report` — последний отчёт с per-action rationale.
* **Текущее состояние**: `steward_status` — pending review, последний запуск, следующий scheduled run.

### Review Inbox
Все действия, требующие одобрения, попадают в stewardship inbox:
* **Просмотр**: `steward_inbox status=pending` — все pending items с urgency ranking.
* **Разрешение**: `steward_inbox_resolve` — merge, mark_outdated, promote, verify, suppress, defer.
* **Типы items**: duplicate_candidate, contradiction_candidate, stale_canonical, drift_detected, promotion_candidate.

### Drift Detection
Периодическая проверка: соответствует ли знание в памяти текущему состоянию кода и документации.
* **Полная проверка**: `drift_scan scope=all` — сверка с repo, docs, changelog.
* **Только canonical**: `drift_scan scope=canonical` — проверить подтверждённые знания.
* **Кандидаты на верификацию**: `verification_candidates` — что давно не проверялось.
* **Ручная верификация**: `verify_entry` — подтвердить или пометить как needs_update.

---

## Правила тегирования и формат

Пиши кратко и конкретно. Избегай общих фраз вроде "обновили код".

**Всегда добавляй теги:**
* **Project**: имя текущего проекта (например, `super-package`).
* **Tech**: используемый стек (например, `laravel-12`, `react`).
* **Category**: `bug`, `architecture`, `deploy`, `performance`, `incident`.
* **Component**: затронутая часть системы (`api`, `worker`, `auth`).

### Пример записи (store_memory):
```json
{
  "content": "Миграция #47 блокирует таблицу orders — использовать CREATE INDEX CONCURRENTLY",
  "type": "episodic",
  "tags": ["postgres", "migrations", "performance"],
  "importance": 0.8
}
```

### Пример решения (store_decision):
```json
{
  "decision": "Переход на chi router вместо gorilla/mux",
  "rationale": "gorilla/mux archived, chi активно поддерживается, совместим по middleware API",
  "consequences": "Нужен рефакторинг middleware chain в 3 сервисах",
  "context": "api-gateway",
  "service": "api-gateway",
  "tags": ["go", "routing", "architecture"]
}
```

---

## Canonical Knowledge

Подтверждённые знания хранятся отдельно от сырых заметок.

* **Просмотр**: `list_canonical_knowledge` или `recall_canonical_knowledge`.
* **Продвижение**: `promote_to_canonical` для записей с высокой уверенностью и подтверждением из нескольких источников.
* **Здоровье**: проверяй через `steward_run scope=canonical` — stale canonical, unverified, conflicting.

Canonical knowledge ранжируется выше при recall и отображается первым в `summarize_project_context`.

---

## Fallback

Если сессия была слишком шумной или неоднозначной, не обновляй основную базу знаний. Используй `save_raw_only`, чтобы сохранить след работы без изменения критических инструкций проекта.
