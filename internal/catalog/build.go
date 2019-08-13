package catalog

import (
	"fmt"
	"strings"

	"github.com/kyleconroy/dinosql/internal/pg"

	"github.com/davecgh/go-spew/spew"
	nodes "github.com/lfittl/pg_query_go/nodes"
)

func ParseRange(rv *nodes.RangeVar) (pg.FQN, error) {
	fqn := pg.FQN{
		Schema: "public",
	}
	if rv.Catalogname != nil {
		fqn.Catalog = *rv.Catalogname
	}
	if rv.Schemaname != nil {
		fqn.Schema = *rv.Schemaname
	}
	if rv.Relname != nil {
		fqn.Rel = *rv.Relname
	} else {
		return fqn, fmt.Errorf("range has empty relname")
	}
	return fqn, nil
}

func ParseList(list nodes.List) (pg.FQN, error) {
	parts := stringSlice(list)
	var fqn pg.FQN
	switch len(parts) {
	case 1:
		fqn = pg.FQN{
			Catalog: "",
			Schema:  "public",
			Rel:     parts[0],
		}
	case 2:
		fqn = pg.FQN{
			Catalog: "",
			Schema:  parts[0],
			Rel:     parts[1],
		}
	case 3:
		fqn = pg.FQN{
			Catalog: parts[0],
			Schema:  parts[1],
			Rel:     parts[2],
		}
	default:
		return fqn, fmt.Errorf("Invalid FQN: %s", join(list, "."))
	}
	return fqn, nil
}

// func getTable(c *pg.Catalog, fqn pg.FQN) (pg.Schema, pg.Table, error) {
// }

func Update(c *pg.Catalog, stmt nodes.Node) error {
	if false {
		spew.Dump(stmt)
	}
	raw, ok := stmt.(nodes.RawStmt)
	if !ok {
		return fmt.Errorf("expected RawStmt; got %T", stmt)
	}
	switch n := raw.Stmt.(type) {

	case nodes.AlterObjectSchemaStmt:
		switch n.ObjectType {

		case nodes.OBJECT_TABLE:
			fqn, err := ParseRange(n.Relation)
			if err != nil {
				return err
			}
			from, exists := c.Schemas[fqn.Schema]
			if !exists {
				return pg.ErrorSchemaDoesNotExist(fqn.Schema)
			}
			table, exists := from.Tables[fqn.Rel]
			if !exists {
				return pg.ErrorRelationDoesNotExist(fqn.Rel)
			}
			to, exists := c.Schemas[*n.Newschema]
			if !exists {
				return pg.ErrorSchemaDoesNotExist(*n.Newschema)
			}
			// Move the table
			delete(from.Tables, fqn.Rel)
			to.Tables[fqn.Rel] = table

		}

	case nodes.AlterTableStmt:
		fqn, err := ParseRange(n.Relation)
		if err != nil {
			return err
		}
		schema, exists := c.Schemas[fqn.Schema]
		if !exists {
			return pg.ErrorSchemaDoesNotExist(fqn.Schema)
		}
		table, exists := schema.Tables[fqn.Rel]
		if !exists {
			return pg.ErrorRelationDoesNotExist(fqn.Rel)
		}

		for _, cmd := range n.Cmds.Items {
			switch cmd := cmd.(type) {
			case nodes.AlterTableCmd:
				idx := -1

				// Lookup column names for column-related commands
				switch cmd.Subtype {
				case nodes.AT_AlterColumnType,
					nodes.AT_DropColumn,
					nodes.AT_DropNotNull,
					nodes.AT_SetNotNull:

					for i, c := range table.Columns {
						if c.Name == *cmd.Name {
							idx = i
							break
						}
					}
					if idx < 0 && !cmd.MissingOk {
						return pg.ErrorColumnDoesNotExist(table.Name, *cmd.Name)
					}
					// If a missing column is allowed, skip this command
					if idx < 0 && cmd.MissingOk {
						continue
					}
				}

				switch cmd.Subtype {

				case nodes.AT_AddColumn:
					d := cmd.Def.(nodes.ColumnDef)
					for _, c := range table.Columns {
						if c.Name == *d.Colname {
							return pg.ErrorColumnAlreadyExists(table.Name, *d.Colname)
						}
					}
					table.Columns = append(table.Columns, pg.Column{
						Name:     *d.Colname,
						DataType: join(d.TypeName.Names, "."),
						NotNull:  isNotNull(d),
						IsArray:  isArray(d.TypeName),
					})

				case nodes.AT_AlterColumnType:
					d := cmd.Def.(nodes.ColumnDef)
					table.Columns[idx].DataType = join(d.TypeName.Names, ".")
					table.Columns[idx].IsArray = isArray(d.TypeName)

				case nodes.AT_DropColumn:
					table.Columns = append(table.Columns[:idx], table.Columns[idx+1:]...)

				case nodes.AT_DropNotNull:
					table.Columns[idx].NotNull = false

				case nodes.AT_SetNotNull:
					table.Columns[idx].NotNull = true

				}

				schema.Tables[fqn.Rel] = table
			}
		}

	case nodes.CreateStmt:
		fqn, err := ParseRange(n.Relation)
		if err != nil {
			return err
		}
		schema, exists := c.Schemas[fqn.Schema]
		if !exists {
			return pg.ErrorSchemaDoesNotExist(fqn.Schema)
		}
		if _, exists := schema.Tables[fqn.Rel]; exists {
			return pg.ErrorRelationAlreadyExists(fqn.Rel)
		}
		table := pg.Table{
			Name: fqn.Rel,
		}
		for _, elt := range n.TableElts.Items {
			switch n := elt.(type) {
			case nodes.ColumnDef:
				colName := *n.Colname
				table.Columns = append(table.Columns, pg.Column{
					Name:     colName,
					DataType: join(n.TypeName.Names, "."),
					NotNull:  isNotNull(n),
					IsArray:  isArray(n.TypeName),
				})
			}
		}
		schema.Tables[fqn.Rel] = table

	case nodes.CreateEnumStmt:
		fqn, err := ParseList(n.TypeName)
		if err != nil {
			return err
		}
		schema, exists := c.Schemas[fqn.Schema]
		if !exists {
			return pg.ErrorSchemaDoesNotExist(fqn.Schema)
		}
		if _, exists := schema.Enums[fqn.Rel]; exists {
			return pg.ErrorTypeAlreadyExists(fqn.Rel)
		}
		schema.Enums[fqn.Rel] = pg.Enum{
			Name: fqn.Rel,
			Vals: stringSlice(n.Vals),
		}

	case nodes.CreateSchemaStmt:
		name := *n.Schemaname
		if _, exists := c.Schemas[name]; exists {
			return pg.ErrorSchemaAlreadyExists(name)
		}
		c.Schemas[name] = pg.NewSchema()

	case nodes.DropStmt:
		for _, obj := range n.Objects.Items {
			if n.RemoveType == nodes.OBJECT_TABLE || n.RemoveType == nodes.OBJECT_TYPE {
				var fqn pg.FQN
				var err error

				switch o := obj.(type) {
				case nodes.List:
					fqn, err = ParseList(o)
				case nodes.TypeName:
					fqn, err = ParseList(o.Names)
				default:
					return fmt.Errorf("nodes.DropStmt: unknown node in objects list: %T", o)
				}
				if err != nil {
					return err
				}

				schema, exists := c.Schemas[fqn.Schema]
				if !exists {
					return pg.ErrorSchemaDoesNotExist(fqn.Schema)
				}

				switch n.RemoveType {
				case nodes.OBJECT_TABLE:
					if _, exists := schema.Tables[fqn.Rel]; exists {
						delete(schema.Tables, fqn.Rel)
					} else if !n.MissingOk {
						return pg.ErrorRelationDoesNotExist(fqn.Rel)
					}

				case nodes.OBJECT_TYPE:
					if _, exists := schema.Enums[fqn.Rel]; exists {
						delete(schema.Enums, fqn.Rel)
					} else if !n.MissingOk {
						return pg.ErrorTypeDoesNotExist(fqn.Rel)
					}

				}

			}

			if n.RemoveType == nodes.OBJECT_SCHEMA {
				var name string
				switch o := obj.(type) {
				case nodes.String:
					name = o.Str
				default:
					return fmt.Errorf("nodes.DropStmt: unknown node in objects list: %T", o)
				}
				if _, exists := c.Schemas[name]; exists {
					delete(c.Schemas, name)
				} else if !n.MissingOk {
					return pg.ErrorSchemaDoesNotExist(name)
				}
			}
		}

	case nodes.RenameStmt:
		switch n.RenameType {
		case nodes.OBJECT_COLUMN:
			fqn, err := ParseRange(n.Relation)
			if err != nil {
				return err
			}
			schema, exists := c.Schemas[fqn.Schema]
			if !exists {
				return pg.ErrorSchemaDoesNotExist(fqn.Schema)
			}
			table, exists := schema.Tables[fqn.Rel]
			if !exists {
				return pg.ErrorRelationDoesNotExist(fqn.Rel)
			}
			idx := -1
			for i, c := range table.Columns {
				if c.Name == *n.Subname {
					idx = i
				}
				if c.Name == *n.Newname {
					return pg.ErrorColumnAlreadyExists(table.Name, c.Name)
				}
			}
			if idx < 0 {
				return pg.ErrorColumnDoesNotExist(table.Name, *n.Subname)
			}
			table.Columns[idx].Name = *n.Newname

		case nodes.OBJECT_TABLE:
			fqn, err := ParseRange(n.Relation)
			if err != nil {
				return err
			}
			schema, exists := c.Schemas[fqn.Schema]
			if !exists {
				return pg.ErrorSchemaDoesNotExist(fqn.Schema)
			}
			table, exists := schema.Tables[fqn.Rel]
			if !exists {
				return pg.ErrorRelationDoesNotExist(fqn.Rel)
			}
			if _, exists := schema.Tables[*n.Newname]; exists {
				return pg.ErrorRelationAlreadyExists(*n.Newname)
			}

			// Remove the table under the old name
			delete(schema.Tables, fqn.Rel)

			// Add the table under the new name
			table.Name = *n.Newname
			schema.Tables[*n.Newname] = table
		}

	case nodes.CreateFunctionStmt:
		fqn, err := ParseList(n.Funcname)
		if err != nil {
			return err
		}
		schema, exists := c.Schemas[fqn.Schema]
		if !exists {
			return pg.ErrorSchemaDoesNotExist(fqn.Schema)
		}
		// TODO arity is not sufficent to identify a function, foo(x BOOL) and
		// foo(y TEXT) are two different functions that can exist at the same time
		arity := len(n.Parameters.Items)
		if _, err := c.LookupFunctionN(fqn, arity); err == nil {
			// we could check n.Replace, but we don't care for the function body,
			// so do nothing
			return nil
		}
		// TODO: support return parameter:
		// CREATE FUNCTION foo(bar TEXT, OUT quz bool) AS $$ SELECT true $$ LANGUAGE sql;
		schema.Funcs[fqn.Rel] = append(schema.Funcs[fqn.Rel], pg.Function{
			Name:       fqn.Rel,
			ArgN:       arity,
			ReturnType: join(n.ReturnType.Names, "."),
		})
	}
	return nil
}

func stringSlice(list nodes.List) []string {
	items := []string{}
	for _, item := range list.Items {
		if n, ok := item.(nodes.String); ok {
			items = append(items, n.Str)
		}
	}
	return items
}

func join(list nodes.List, sep string) string {
	return strings.Join(stringSlice(list), sep)
}

func isArray(n *nodes.TypeName) bool {
	if n == nil {
		return false
	}
	return len(n.ArrayBounds.Items) > 0
}

func isNotNull(n nodes.ColumnDef) bool {
	if n.IsNotNull {
		return true
	}
	for _, c := range n.Constraints.Items {
		switch n := c.(type) {
		case nodes.Constraint:
			if n.Contype == nodes.CONSTR_NOTNULL {
				return true
			}
			if n.Contype == nodes.CONSTR_PRIMARY {
				return true
			}
		}
	}
	return false
}

func ToColumn(n *nodes.TypeName) pg.Column {
	if n == nil {
		panic("can't build column for nil type name")
	}
	return pg.Column{
		DataType: join(n.Names, "."),
		NotNull:  true, // XXX: How do we know if this should be null?
		IsArray:  isArray(n),
	}
}