-- People who sign in through their company's identity provider with OpenID Connect.
--
-- Such a person is a principal of kind person, found by the issuer and the subject the provider's ID token
-- names, never by an email alone. The email, and whether the provider marked it verified, are kept to show
-- who the person is and to find them by it; each sign-in records them again, since the provider may change
-- them. A person who signs in this way has no password, and a demo person has no issuer.
ALTER TABLE principals
    ADD COLUMN oidc_issuer text CHECK (length(oidc_issuer) <= 2048 AND oidc_issuer ~ '^https://[!-~]+$'),
    ADD COLUMN oidc_subject text CHECK (oidc_subject ~ '^[!-~]{1,255}$'),
    ADD COLUMN email text CHECK (length(email) BETWEEN 3 AND 320 AND email !~ '[[:cntrl:][:space:]]'),
    ADD COLUMN email_verified boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT principals_oidc_identity CHECK ((oidc_issuer IS NULL) = (oidc_subject IS NULL)),
    ADD CONSTRAINT principals_oidc_person CHECK (oidc_subject IS NULL OR (kind = 'person' AND login IS NULL)),
    ADD CONSTRAINT principals_oidc_email CHECK ((email IS NULL OR oidc_subject IS NOT NULL)
        AND (NOT email_verified OR email IS NOT NULL)),
    ADD CONSTRAINT principals_oidc_unique UNIQUE (workspace_id, oidc_issuer, oidc_subject);

-- A principal keeps the workspace, the kind and the sign-in identity it was made with, for any writer: a
-- demo login never becomes a provider's subject, and a subject never passes to another principal.
CREATE FUNCTION gtm_principal_identity_fixed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.workspace_id, NEW.workspace_kind, NEW.kind, NEW.login, NEW.oidc_issuer, NEW.oidc_subject)
       IS DISTINCT FROM
       (OLD.workspace_id, OLD.workspace_kind, OLD.kind, OLD.login, OLD.oidc_issuer, OLD.oidc_subject) THEN
        RAISE EXCEPTION 'a principal keeps the identity it was made with' USING ERRCODE = 'GT021';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER principals_identity_fixed BEFORE UPDATE ON principals
    FOR EACH ROW EXECUTE FUNCTION gtm_principal_identity_fixed();
