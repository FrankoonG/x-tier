import { useEffect, useMemo, useState } from 'react';
import {
  Badge,
  Banner,
  Button,
  Card,
  CardBody,
  CardHeader,
  Code,
  EmptyState,
  Field,
  FormActions,
  IconCheck,
  IconClose,
  IconPlus,
  IconRefresh,
  IconTrash,
  InlineMessage,
  Input,
  PageHeader,
  Row,
  Screen,
  ScrollArea,
  SegmentedControl,
  Table,
  Tag,
  Tooltip,
} from '@stratum/ui';
import type { BadgeVariant, TableColumn } from '@stratum/ui';
import {
  getXrayProfiles,
  mutations,
  validateXrayProfile,
} from '../api/control';
import { describeFailure, type FailureView } from '../api/errors';
import type { XrayProfile, XrayProfilesResponse } from '../api/types';
import { FailureNotice } from '../components/FailureNotice';
import { MutationDialog, useMutationDialog } from '../components/MutationDialog';
import { useControl } from '../state/store';
import { useDomainRead } from '../state/useDomainRead';
import { discardProfileDraft, type ProfileKind } from './profileDraft';

type SupportedKind = ProfileKind;

interface ValidationResponse {
  ok: true;
  revision: number;
  profile: string;
}

interface ValidationReading {
  revision: number;
}

const SUPPORT: Record<
  SupportedKind,
  {
    label: string;
    role: string;
    transport: string;
    security: string;
    variant: BadgeVariant;
  }
> = {
  vless: {
    label: 'VLESS',
    role: 'Private node interconnect',
    transport: 'TCP',
    security: 'Plaintext; private networks only',
    variant: 'warning',
  },
  socks: {
    label: 'SOCKS5',
    role: 'Authenticated client proxy',
    transport: 'CONNECT / TCP',
    security: 'Username and password',
    variant: 'info',
  },
};

const isSupported = (kind: string): kind is SupportedKind => kind === 'vless' || kind === 'socks';

export function Profiles() {
  const { revision, epoch, refresh } = useControl();
  const read = useDomainRead<XrayProfilesResponse>(
    'xray-profiles', getXrayProfiles, [revision, epoch],
  );
  const mutation = useMutationDialog();

  const [creating, setCreating] = useState(false);
  const [draft, setDraft] = useState(discardProfileDraft);
  const { kind, id, username, credential, submitted } = draft;
  const [validatingID, setValidatingID] = useState<string | null>(null);
  const [validated, setValidated] = useState<Record<string, ValidationReading>>({});
  const [validationFailure, setValidationFailure] = useState<FailureView | null>(null);

  const profiles = useMemo(
    () =>
      read.data
        ? Object.values(read.data.xray_profiles ?? {}).sort((a, b) => a.id.localeCompare(b.id))
        : null,
    [read.data],
  );

  useEffect(() => {
    setValidated({});
    setValidationFailure(null);
  }, [revision, epoch]);

  const trimmedID = id.trim();
  const trimmedUsername = username.trim();
  const trimmedCredential = credential.trim();
  const idError = submitted && !trimmedID ? 'Profile ID is required.' : null;
  const usernameError = submitted && kind === 'socks' && !trimmedUsername
    ? 'A username is required for authenticated SOCKS5.'
    : null;
  const credentialError = submitted && !trimmedCredential
    ? 'A credential is required.'
    : null;
  const formReady = Boolean(
    trimmedID && trimmedCredential && (kind === 'vless' || trimmedUsername),
  );
  const replacing = Boolean(profiles?.some((profile) => profile.id === trimmedID));

  const resetForm = () => {
    setCreating(false);
    setDraft(discardProfileDraft);
  };

  // Closing a preview means abandoning the whole draft. This clears both the
  // controlled credential input state and the operation spec that captured it.
  const discardPreview = () => {
    mutation.close();
    resetForm();
  };

  const addOperation = () => mutations.xrayProfilePut(
    kind === 'vless'
      ? {
          id: trimmedID,
          kind: 'vless',
          credential: trimmedCredential,
          transport: 'tcp',
          security: 'none',
          allowInsecurePlaintext: true,
        }
      : {
          id: trimmedID,
          kind: 'socks',
          username: trimmedUsername,
          credential: trimmedCredential,
        },
  );

  const validate = async (profile: XrayProfile) => {
    setValidatingID(profile.id);
    setValidationFailure(null);
    try {
      const result: ValidationResponse = await validateXrayProfile(profile.id);
      setValidated((current) => ({ ...current, [profile.id]: { revision: result.revision } }));
    } catch (error) {
      setValidated((current) => {
        const next = { ...current };
        delete next[profile.id];
        return next;
      });
      setValidationFailure(describeFailure(error));
    } finally {
      setValidatingID(null);
    }
  };

  const columns: TableColumn<XrayProfile>[] = [
    {
      key: 'id',
      header: 'Profile ID',
      cell: (profile) => <Code variant="plain">{profile.id}</Code>,
    },
    {
      key: 'kind',
      header: 'Protocol',
      width: 150,
      cell: (profile) =>
        isSupported(profile.kind) ? (
          <Tag size="sm" variant={SUPPORT[profile.kind].variant}>
            {SUPPORT[profile.kind].label}
          </Tag>
        ) : (
          <Tag size="sm" variant="danger" outline>
            {profile.kind || 'unknown'}
          </Tag>
        ),
    },
    {
      key: 'use',
      header: 'Supported use',
      width: 230,
      cell: (profile) =>
        isSupported(profile.kind) ? SUPPORT[profile.kind].role : 'Unsupported by this build',
    },
    {
      key: 'transport',
      header: 'Transport',
      width: 150,
      cell: (profile) =>
        isSupported(profile.kind) ? SUPPORT[profile.kind].transport : 'Unavailable',
    },
    {
      key: 'security',
      header: 'Credential boundary',
      width: 260,
      cell: (profile) =>
        isSupported(profile.kind) ? SUPPORT[profile.kind].security : 'Cannot be configured here',
    },
    {
      key: 'validation',
      header: 'Validation',
      width: 150,
      cell: (profile) => {
        const reading = validated[profile.id];
        return reading?.revision === revision ? (
          <Tag size="sm" variant="success" icon={<IconCheck />}>
            rev {reading.revision}
          </Tag>
        ) : (
          <span style={{ color: 'var(--stratum-text-subtle)', fontSize: 'var(--stratum-text-xs)' }}>
            not checked
          </span>
        );
      },
    },
    {
      key: 'actions',
      header: '',
      headerLabel: 'Profile actions',
      width: 190,
      align: 'end',
      cell: (profile) => (
        <Row gap="var(--stratum-space-3)" wrap={false}>
          <Button
            size="xs"
            variant="default"
            icon={<IconCheck />}
            loading={validatingID === profile.id}
            disabled={validatingID !== null}
            onClick={() => void validate(profile)}
          >
            Validate
          </Button>
          <Tooltip
            trigger={
              <Button
                size="xs"
                variant="ghost"
                iconOnly
                icon={<IconTrash />}
                aria-label={`Remove profile ${profile.id}`}
                onClick={() =>
                  mutation.open({
                    operation: mutations.xrayProfileRemove(profile.id),
                    title: `Remove ${profile.id}`,
                    description:
                      'Deletes this transport profile. The daemon refuses removal while another configuration entry still references it.',
                    confirmLabel: 'Remove',
                    destructive: true,
                    summarise: () => (
                      <>
                        Profile <Code>{profile.id}</Code> will be removed. This cannot be undone from
                        the panel.
                      </>
                    ),
                  })
                }
              />
            }
          >
            Remove profile
          </Tooltip>
        </Row>
      ),
    },
  ];

  return (
    <Screen
      header={
        <PageHeader
          title="Xray profiles"
          description="Stored transport identities used by private node links and authenticated SOCKS5 clients."
          meta={
            profiles ? (
              <Badge variant="neutral" size="sm">
                {profiles.length} {profiles.length === 1 ? 'profile' : 'profiles'}
              </Badge>
            ) : (
              <Badge variant="unknown" size="sm">
                not read
              </Badge>
            )
          }
          actions={
            !creating ? (
              <Button
                size="sm"
                variant="primary"
                icon={<IconPlus />}
                onClick={() => setCreating(true)}
              >
                Add profile
              </Button>
            ) : undefined
          }
        />
      }
    >
      <Banner variant="warning" size="sm" title="Two TCP-only profile types">
        This build creates only private plaintext VLESS over TCP and authenticated SOCKS5 CONNECT
        over TCP. It provides no TLS, REALITY, WebSocket, QUIC, UDP, unauthenticated SOCKS, or other
        protocol options.
      </Banner>

      {read.failure && (
        <FailureNotice
          failure={read.failure}
          actions={
            !read.failure.blocked ? (
              <Button
                size="sm"
                variant="default"
                icon={<IconRefresh />}
                onClick={() => void read.reload()}
              >
                Try again
              </Button>
            ) : undefined
          }
        />
      )}

      {validationFailure && <FailureNotice failure={validationFailure} />}

      {creating && (
        <Card variant="outlined">
          <CardHeader
            headingLevel={2}
            title="Add an Xray profile"
            description="The credential is sent only in the authenticated mutation and is never returned by the control API."
            actions={
              <Button
                size="sm"
                variant="ghost"
                iconOnly
                icon={<IconClose />}
                aria-label="Stop adding a profile"
                onClick={resetForm}
              />
            }
          />
          <CardBody>
            <form
              onSubmit={(event) => {
                event.preventDefault();
                setDraft((current) => ({ ...current, submitted: true }));
                if (!formReady) return;
                mutation.open({
                  operation: addOperation(),
                  title: `${replacing ? 'Replace' : 'Add'} ${trimmedID}`,
                  description: replacing
                    ? 'A profile with this ID already exists. Applying replaces it with the selected supported profile shape.'
                    : `Adds a ${SUPPORT[kind].label} profile to the stored configuration.`,
                  confirmLabel: replacing ? 'Replace' : 'Add',
                  destructive: replacing,
                  summarise: () => (
                    <>
                      Profile <Code>{trimmedID}</Code> will use <Code>{SUPPORT[kind].label}</Code>{' '}
                      with {SUPPORT[kind].transport}. The credential value is redacted from the
                      dry-run result and every subsequent read.
                    </>
                  ),
                });
              }}
              style={{ display: 'grid', gap: 'var(--stratum-space-8)', maxWidth: '48rem' }}
            >
              <Field label="Profile type" required group>
                <SegmentedControl
                  label="Profile type"
                  value={kind}
                  onValueChange={(value) =>
                    setDraft((current) => ({ ...current, kind: value as SupportedKind }))
                  }
                  items={[
                    { value: 'vless', children: 'VLESS / TCP' },
                    { value: 'socks', children: 'SOCKS5 CONNECT / TCP' },
                  ]}
                  fullWidth
                />
              </Field>

              <Field
                label="Profile ID"
                required
                error={idError}
                description="The stable identifier referenced by peers and inbounds."
              >
                <Input
                  value={id}
                  onChange={(event) =>
                    setDraft((current) => ({ ...current, id: event.currentTarget.value }))
                  }
                  placeholder="private-edge"
                  invalid={Boolean(idError)}
                  required
                  fullWidth
                />
              </Field>

              {kind === 'socks' && (
                <Field
                  label="SOCKS username"
                  required
                  error={usernameError}
                  description="SOCKS profiles created here always require username/password authentication."
                >
                  <Input
                    value={username}
                    onChange={(event) =>
                      setDraft((current) => ({ ...current, username: event.currentTarget.value }))
                    }
                    autoComplete="off"
                    invalid={Boolean(usernameError)}
                    required
                    fullWidth
                  />
                </Field>
              )}

              <Field
                label={kind === 'vless' ? 'UUID credential' : 'SOCKS password'}
                required
                error={credentialError}
                description="Submitted once to the daemon. Stored profile reads and dry-run responses never include this value."
              >
                <Input
                  type="password"
                  value={credential}
                  onChange={(event) =>
                    setDraft((current) => ({ ...current, credential: event.currentTarget.value }))
                  }
                  autoComplete="new-password"
                  spellCheck={false}
                  invalid={Boolean(credentialError)}
                  required
                  fullWidth
                />
              </Field>

              {kind === 'vless' ? (
                <InlineMessage variant="warning" size="xs">
                  VLESS is fixed to TCP with security=none and the daemon's explicit insecure
                  plaintext opt-in. Use it only on a private, protected network.
                </InlineMessage>
              ) : (
                <InlineMessage variant="info" size="xs">
                  SOCKS is fixed to authenticated SOCKS5 CONNECT over TCP. UDP ASSOCIATE and
                  unauthenticated operation are not offered.
                </InlineMessage>
              )}

              {replacing && (
                <InlineMessage variant="warning" size="xs">
                  <Code>{trimmedID}</Code> already exists. The add command replaces that profile in
                  place if the daemon accepts the change.
                </InlineMessage>
              )}

              <FormActions align="end" divider>
                <Button type="button" variant="subtle" onClick={resetForm}>
                  Cancel
                </Button>
                <Button type="submit" variant="primary" disabled={!formReady}>
                  Review change
                </Button>
              </FormActions>
            </form>
          </CardBody>
        </Card>
      )}

      <Card variant="outlined" padding="none">
        <CardHeader
          padding="sm"
          headingLevel={2}
          title="Stored profiles"
          description="Credential values are redacted by the typed control API and are not inspectable here."
        />
        <CardBody padding="none">
          <ScrollArea orientation="both" maxHeight="34rem" label="Stored Xray profiles">
            <Table
              data={profiles ?? []}
              columns={columns}
              rowKey={(profile) => profile.id}
              layout="auto"
              density="default"
              stickyHeader
              zebra
              loading={read.pristine && read.loading}
              caption="Xray transport profiles stored in the configuration"
              emptyState={
                <EmptyState
                  title={read.data ? 'No profiles configured' : 'Profiles not read'}
                  headingLevel={3}
                  description={
                    read.data
                      ? 'Add a VLESS/TCP private interconnect or authenticated SOCKS5 CONNECT/TCP profile.'
                      : 'The daemon did not return the profile map, so its contents are unknown.'
                  }
                />
              }
            />
          </ScrollArea>
        </CardBody>
      </Card>

      <MutationDialog
        spec={mutation.spec}
        onClose={discardPreview}
        onApplied={() => {
          resetForm();
          setValidated({});
          setValidationFailure(null);
          void refresh();
        }}
      />
    </Screen>
  );
}
