-- ============================================================
-- Migración 001: Álbumes privados y sistema de créditos
-- Ejecutar en Supabase SQL Editor
-- ============================================================

-- 1. Agregar columna credits a profiles
--    El backend asigna 100 explícitamente al registrarse (registro en credit_transactions)
ALTER TABLE public.profiles
ADD COLUMN IF NOT EXISTS credits INTEGER NOT NULL DEFAULT 0;

-- 2. Historial de transacciones de créditos
CREATE TABLE IF NOT EXISTS public.credit_transactions (
    id         UUID        DEFAULT gen_random_uuid() PRIMARY KEY,
    user_id    UUID        NOT NULL REFERENCES public.profiles(id) ON DELETE CASCADE,
    amount     INTEGER     NOT NULL,   -- positivo = recibió, negativo = gastó
    action     TEXT        NOT NULL,   -- ej: 'registration_bonus', 'spend_action'
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL
);

-- 3. Fotos del álbum privado
CREATE TABLE IF NOT EXISTS public.photos (
    id         UUID        DEFAULT gen_random_uuid() PRIMARY KEY,
    user_id    UUID        NOT NULL REFERENCES public.profiles(id) ON DELETE CASCADE,
    url        TEXT        NOT NULL,
    caption    TEXT,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL
);

-- 4. Solicitudes de acceso al álbum
CREATE TABLE IF NOT EXISTS public.album_access_requests (
    id           UUID        DEFAULT gen_random_uuid() PRIMARY KEY,
    from_user_id UUID        NOT NULL REFERENCES public.profiles(id) ON DELETE CASCADE,
    to_user_id   UUID        NOT NULL REFERENCES public.profiles(id) ON DELETE CASCADE,
    status       TEXT        NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'accepted', 'rejected')),
    created_at   TIMESTAMPTZ DEFAULT now() NOT NULL,
    responded_at TIMESTAMPTZ
);

-- Solo puede haber una solicitud pendiente entre dos usuarios
CREATE UNIQUE INDEX IF NOT EXISTS idx_album_access_requests_pending
    ON public.album_access_requests (from_user_id, to_user_id)
    WHERE status = 'pending';

-- 5. Accesos concedidos al álbum
CREATE TABLE IF NOT EXISTS public.album_access (
    owner_id   UUID        NOT NULL REFERENCES public.profiles(id) ON DELETE CASCADE,
    viewer_id  UUID        NOT NULL REFERENCES public.profiles(id) ON DELETE CASCADE,
    granted_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    PRIMARY KEY (owner_id, viewer_id)
);

-- 6. Índices de rendimiento
CREATE INDEX IF NOT EXISTS idx_photos_user_id
    ON public.photos (user_id);

CREATE INDEX IF NOT EXISTS idx_album_access_requests_to_user
    ON public.album_access_requests (to_user_id)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_credit_transactions_user_id
    ON public.credit_transactions (user_id);
