export type Value = any;
export type Field = { name: string; alias: string; args: Record<string, Value>; children: Field[]; directives: { name: string; args: Record<string, Value> }[]; condition?: string; spread?: string };
export type Operation = { type: string; name: string; defaults: Record<string, Value>; fields: Field[] };
export type Document = { operations: Operation[]; fragments: Map<string, Field> };

class Parser {
  tokens: string[] = [];
  position = 0;
  depth = 0;

  constructor(source: string) {
    if (source.length > 1024 * 1024) throw new Error('Query too large');
    const pattern = /\s+|,|#[^\n\r]*|\.\.\.|"(?:\\.|[^"\\])*"|[A-Za-z_][A-Za-z_0-9]*|-?\d+(?:\.\d+)?|[!$():=@\[\]{}|]/gy;
    while (pattern.lastIndex < source.length) {
      const match = pattern.exec(source);
      if (!match) throw new Error('Invalid GraphQL token');
      const token = match[0];
      if (!/^[\s,#]/.test(token)) this.tokens.push(token);
    }
  }

  peek(): string { return this.tokens[this.position] ?? ''; }
  take(): string {
    const token = this.tokens[this.position++];
    if (!token) throw new Error('Unexpected end of GraphQL document');
    return token;
  }
  expect(token: string): void {
    if (this.take() !== token) throw new Error(`Expected ${token}`);
  }
  name(): string {
    const value = this.take();
    if (!/^[_A-Za-z][_A-Za-z0-9]*$/.test(value)) throw new Error('Expected a field name');
    return value;
  }

  value(): Value {
    const token = this.take();
    if (token === '$') return { variable: this.name() };
    if (token.startsWith('"')) return JSON.parse(token);
    if (token === '[') {
      const items: Value[] = [];
      while (this.peek() !== ']') items.push(this.value());
      this.expect(']'); return items;
    }
    if (token === '{') {
      const obj: Record<string, Value> = {};
      while (this.peek() !== '}') { const key = this.name(); this.expect(':'); obj[key] = this.value(); }
      this.expect('}'); return obj;
    }
    if (token === 'true' || token === 'false') return token === 'true';
    if (token === 'null') return null;
    if (/^-?\d/.test(token)) return Number(token);
    if (!/^[_A-Za-z]/.test(token)) throw new Error('Invalid GraphQL value');
    return token;
  }

  args(): Record<string, Value> {
    const args: Record<string, Value> = {};
    if (this.peek() !== '(') return args;
    this.take();
    while (this.peek() !== ')') { const key = this.name(); this.expect(':'); args[key] = this.value(); }
    this.expect(')'); return args;
  }

  directives(): { name: string; args: Record<string, Value> }[] {
    const result: { name: string; args: Record<string, Value> }[] = [];
    while (this.peek() === '@') { this.take(); result.push({ name: this.name(), args: this.args() }); }
    return result;
  }

  selection(): Field[] {
    if (++this.depth > 64) throw new Error('GraphQL selection too deep');
    this.expect('{');
    const fields: Field[] = [];
    while (this.peek() !== '}') {
      if (this.peek() === '...') {
        this.take();
        const name = this.name();
        const field: Field = { name: '', alias: '', args: {}, children: [], directives: [] };
        if (name === 'on') field.condition = this.name(); else field.spread = name;
        field.directives = this.directives();
        if (!field.spread) field.children = this.selection();
        fields.push(field);
        continue;
      }
      let name = this.name();
      const alias = name;
      if (this.peek() === ':') { this.take(); name = this.name(); }
      const args = this.args(), directives = this.directives();
      fields.push({ name, alias, args, directives, children: this.peek() === '{' ? this.selection() : [] });
    }
    this.expect('}'); --this.depth;
    return fields;
  }

  parse(): Document {
    const operations: Operation[] = [], fragments = new Map<string, Field>();
    while (this.peek()) {
      if (this.peek() === 'fragment') {
        this.take(); const name = this.name(); this.expect('on'); const condition = this.name();
        const directives = this.directives(), children = this.selection();
        fragments.set(name, { name: '', alias: '', args: {}, directives, children, condition });
        continue;
      }
      let type = 'query', name = '';
      const defaults: Record<string, Value> = {};
      if (this.peek() !== '{') {
        type = this.take();
        if (!['query', 'mutation'].includes(type)) throw new Error('Unsupported operation');
        if (this.peek() !== '(' && this.peek() !== '{') name = this.name();
        if (this.peek() === '(') {
          this.take();
          while (this.peek() !== ')') {
            this.expect('$'); const variable = this.name(); this.expect(':');
            while (this.peek() && !['=', '$', ')'].includes(this.peek())) this.take();
            if (this.peek() === '=') { this.take(); defaults[variable] = this.value(); }
          }
          this.expect(')');
        }
      }
      this.directives();
      operations.push({ type, name, defaults, fields: this.selection() });
    }
    if (!operations.length) throw new Error('GraphQL document has no operation');
    return { operations, fragments };
  }
}

export function parse(source: string): Document { return new Parser(source).parse(); }

export function evaluate(value: Value, variables: Record<string, any>): any {
  if (value === null || typeof value !== 'object') return value;
  if (Array.isArray(value)) return value.map(v => evaluate(v, variables));
  if ('variable' in value && Object.keys(value).length === 1) return variables[value.variable];
  return Object.fromEntries(Object.entries(value).map(([key, val]) => [key, evaluate(val, variables)]));
}
