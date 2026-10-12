export type TemplateRef = {
  id: string;
  name: string;
  description: string;
  enabled: boolean;
  sources: string[];
};
export type SharedGrant = {
  volume: TemplateRef;
  access: "read_only" | "read_write";
  sources: string[];
};
export type ConfigurationPreferences = {
  revision: number;
  network_template_id: string;
  environment_template_id: string;
  show_shared_volumes: boolean;
};
export type ConfigurationSettings = {
  administrator_defaults: {
    network_template_id: string;
    environment_template_id: string;
  };
  networks: TemplateRef[];
  environments: TemplateRef[];
  shared_volumes: SharedGrant[];
  default_network: TemplateRef | null;
  default_environment: TemplateRef | null;
  selected_network: TemplateRef | null;
  selected_environment: TemplateRef | null;
  preferences: ConfigurationPreferences;
  default_revision: number;
  network_conflict: boolean;
  environment_conflict: boolean;
  needs_admin_resolution: boolean;
  network_default_source: string[];
  environment_sources: string[];
};
